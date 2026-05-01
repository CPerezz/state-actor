package geth

import (
	"encoding/json"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"

	"github.com/nerolation/state-actor/genesis"
)

// pathdbSchemaVersion mirrors the on-disk schema constant from
// triedb/pathdb. Geth's startup uses rawdb.ReadDatabaseVersion as a gate
// for "is this DB initialized?" — without this entry the DB looks blank
// and geth silently overwrites it with a fresh dev genesis.
const pathdbSchemaVersion = 9

// prefixWriter wraps a KeyValueWriter to prepend a fixed prefix to all keys.
// Used to write PathDB metadata into the "v" (verkle) namespace.
type prefixWriter struct {
	prefix []byte
	w      ethdb.KeyValueWriter
}

func (pw *prefixWriter) Put(key, value []byte) error {
	return pw.w.Put(append(pw.prefix, key...), value)
}

func (pw *prefixWriter) Delete(key []byte) error {
	return pw.w.Delete(append(pw.prefix, key...))
}

// WritePathDBMetadata persists the metadata that geth's PathDB and snapshot
// layers expect on a freshly populated database. Without these entries geth
// treats the DB as uninitialized and either overwrites it with a fresh
// genesis (missing DatabaseVersion) or triggers a full snapshot
// regeneration (missing/mismatched SnapshotGenerator).
//
// Layout note: in binary trie mode pathdb wraps its diskdb under the "v"
// (rawdb.VerklePrefix) namespace at construction time
// (triedb/pathdb/database.go:168-170), so StateID, PersistentStateID,
// SnapshotRoot, and SnapshotGenerator must be written under that prefix.
// DatabaseVersion is read by geth before pathdb is constructed, so it
// always lives at the raw key.
func WritePathDBMetadata(w ethdb.KeyValueWriter, stateRoot common.Hash, binaryTrie bool) error {
	pathdbWriter := w
	if binaryTrie {
		pathdbWriter = &prefixWriter{prefix: []byte("v"), w: w}
	}
	rawdb.WriteStateID(pathdbWriter, stateRoot, 0)
	rawdb.WritePersistentStateID(pathdbWriter, 0)
	rawdb.WriteSnapshotRoot(pathdbWriter, stateRoot)
	if err := WriteCompletedSnapshotGenerator(pathdbWriter, binaryTrie); err != nil {
		return fmt.Errorf("failed to write snapshot generator: %w", err)
	}
	rawdb.WriteDatabaseVersion(w, pathdbSchemaVersion)
	return nil
}

// WriteGenesisBlock writes the genesis block and associated metadata to the database.
// This is called after state generation with the computed state root.
// When binaryTrie is true, EnableVerkleAtGenesis is set in the chain config
// (legacy field name — it actually enables binary trie mode per EIP-7864).
// The ancientDir is the path for the freezer/ancient database (e.g. "<chaindata>/ancient").
//
// This is geth-specific and lives in client/geth/. The genesis package retains
// only client-neutral parsers (LoadGenesis, ToStateAccounts, GetAllocStorage,
// GetAllocCode).
func WriteGenesisBlock(db ethdb.KeyValueStore, gen *genesis.Genesis, stateRoot common.Hash, binaryTrie bool, ancientDir string) (*types.Block, error) {
	if gen.Config == nil {
		return nil, fmt.Errorf("genesis has no chain config")
	}

	// Determine the chain config to persist. When binaryTrie is true, we
	// enable EIP-7864 binary trie mode (legacy field name: EnableVerkleAtGenesis).
	// We work on a copy so the caller's *Genesis is never mutated.
	chainCfg := gen.Config
	if binaryTrie {
		cfgCopy := *gen.Config
		cfgCopy.EnableVerkleAtGenesis = true
		chainCfg = &cfgCopy
	}

	// Build the genesis block header
	header := &types.Header{
		Number:     new(big.Int).SetUint64(uint64(gen.Number)),
		Nonce:      types.EncodeNonce(uint64(gen.Nonce)),
		Time:       uint64(gen.Timestamp),
		ParentHash: gen.ParentHash,
		Extra:      gen.ExtraData,
		GasLimit:   uint64(gen.GasLimit),
		GasUsed:    uint64(gen.GasUsed),
		Difficulty: (*big.Int)(gen.Difficulty),
		MixDigest:  gen.Mixhash,
		Coinbase:   gen.Coinbase,
		Root:       stateRoot,
	}

	// Set defaults
	if header.GasLimit == 0 {
		header.GasLimit = params.GenesisGasLimit
	}
	if header.Difficulty == nil {
		if gen.Config.Ethash == nil {
			header.Difficulty = big.NewInt(0)
		} else {
			header.Difficulty = params.GenesisDifficulty
		}
	}

	// Handle EIP-1559 base fee
	if gen.Config.IsLondon(common.Big0) {
		if gen.BaseFee != nil {
			header.BaseFee = (*big.Int)(gen.BaseFee)
		} else {
			header.BaseFee = new(big.Int).SetUint64(params.InitialBaseFee)
		}
	}

	var withdrawals []*types.Withdrawal
	num := big.NewInt(int64(gen.Number))
	timestamp := uint64(gen.Timestamp)

	// Handle Shanghai
	if gen.Config.IsShanghai(num, timestamp) {
		emptyWithdrawalsHash := types.EmptyWithdrawalsHash
		header.WithdrawalsHash = &emptyWithdrawalsHash
		withdrawals = make([]*types.Withdrawal, 0)
	}

	// Handle Cancun
	if gen.Config.IsCancun(num, timestamp) {
		header.ParentBeaconRoot = new(common.Hash)
		if gen.ExcessBlobGas != nil {
			excess := uint64(*gen.ExcessBlobGas)
			header.ExcessBlobGas = &excess
		} else {
			header.ExcessBlobGas = new(uint64)
		}
		if gen.BlobGasUsed != nil {
			used := uint64(*gen.BlobGasUsed)
			header.BlobGasUsed = &used
		} else {
			header.BlobGasUsed = new(uint64)
		}
	}

	// Handle Prague
	if gen.Config.IsPrague(num, timestamp) {
		emptyRequestsHash := types.EmptyRequestsHash
		header.RequestsHash = &emptyRequestsHash
	}

	// Create the block
	block := types.NewBlock(header, &types.Body{Withdrawals: withdrawals}, nil, trie.NewStackTrie(nil))

	// Write to database
	batch := db.NewBatch()

	// Marshal genesis alloc for storage (geth expects this)
	allocBlob, err := json.Marshal(gen.Alloc)
	if err != nil {
		return nil, fmt.Errorf("failed to marshal genesis alloc: %w", err)
	}

	// Write all the required rawdb entries
	rawdb.WriteGenesisStateSpec(batch, block.Hash(), allocBlob)
	rawdb.WriteBlock(batch, block)
	rawdb.WriteReceipts(batch, block.Hash(), block.NumberU64(), nil)
	rawdb.WriteCanonicalHash(batch, block.Hash(), block.NumberU64())
	rawdb.WriteHeadBlockHash(batch, block.Hash())
	rawdb.WriteHeadFastBlockHash(batch, block.Hash())
	rawdb.WriteHeadHeaderHash(batch, block.Hash())
	rawdb.WriteChainConfig(batch, block.Hash(), chainCfg)

	if err := WritePathDBMetadata(batch, stateRoot, binaryTrie); err != nil {
		return nil, err
	}

	if err := batch.Write(); err != nil {
		return nil, fmt.Errorf("failed to write genesis block: %w", err)
	}

	// Initialize the ancient/freezer database. Geth requires the freezer
	// directory with proper index files to exist, even for a genesis-only
	// database. rawdb.Open wraps the key-value store with a chain freezer
	// and creates the necessary .cidx/.ridx/.meta table files.
	if ancientDir != "" {
		fdb, err := rawdb.Open(db, rawdb.OpenOptions{Ancient: ancientDir})
		if err != nil {
			return nil, fmt.Errorf("failed to initialize ancient database: %w", err)
		}
		fdb.Close()
	}

	return block, nil
}

// snapshotGenerator mirrors the wire format of pathdb's unexported
// journalGenerator. The field order, types, and naming must match
// triedb/pathdb/journal.go exactly so RLP-encoded blobs round-trip.
//
// IsBintrie is rlp:"optional" upstream too: legacy v3 entries decode with
// the field defaulted to false, which is the right answer for any merkle
// database that wrote them.
type snapshotGenerator struct {
	Wiping    bool // deprecated, kept for backward compatibility
	Done      bool
	Marker    []byte
	Accounts  uint64
	Slots     uint64
	Storage   uint64
	IsBintrie bool `rlp:"optional"`
}

// WriteCompletedSnapshotGenerator persists a SnapshotGenerator entry marking
// the snapshot as fully generated (Done=true, nil marker).
//
// Without this entry, pathdb's loadGenerator returns a nil generator on open,
// and setStateGenerator constructs a fresh one with an empty (non-nil) marker.
// The disk layer's genComplete() then reports false, which:
//   - in MPT mode (noBuild=false), triggers a full snapshot regeneration
//     from scratch, and
//   - in binary trie mode (noBuild=true via isVerkle), prevents AccountIterator
//     and SnapshotCompleted from succeeding.
//
// The generator's binary-trie-ness is encoded both by writing under the "v"
// (rawdb.VerklePrefix) namespace via a prefixWriter and by setting
// IsBintrie=true in the RLP blob. pathdb/journal.go enforces a scheme match
// on the IsBintrie field (triedb/pathdb/journal.go:163-171) and discards
// generators whose flag does not match the opening database's mode.
func WriteCompletedSnapshotGenerator(w ethdb.KeyValueWriter, isBintrie bool) error {
	blob, err := rlp.EncodeToBytes(snapshotGenerator{Done: true, IsBintrie: isBintrie})
	if err != nil {
		return fmt.Errorf("encode snapshot generator: %w", err)
	}
	rawdb.WriteSnapshotGenerator(w, blob)
	return nil
}
