//go:build cgo_besu

package besu

import (
	"context"
	"encoding/binary"
	"fmt"
	"log"
	mrand "math/rand"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/besu"
	besurlp "github.com/nerolation/state-actor/internal/besu/rlp"
	besutrie "github.com/nerolation/state-actor/internal/besu/trie"
	"github.com/nerolation/state-actor/internal/entitygen"
	"github.com/nerolation/state-actor/internal/streamingtrie"
	"github.com/nerolation/state-actor/internal/streamsort"
)

// writeStateAndCollectRoot drives the two-phase streaming pipeline.
//
// Phase 1 generates entities via a single-goroutine RNG (math/rand.Rand is
// not thread-safe) and writes (addrHash → entityBlob) to a temp Pebble DB,
// which auto-sorts by key. Phase 2 iterates the sorted DB, builds per-account
// storage tries, writes flat state and code, and feeds (addrHash, accountRLP)
// into the account trie builder. SaveWorldState is invoked here at the end.
//
// Memory bound: O(max storage slots per single contract). The full account
// set never lives in RAM at once.
func writeStateAndCollectRoot(
	ctx context.Context,
	cfg generator.Config,
	db *besuDB,
	sink *nodeSink,
) (common.Hash, []byte, *generator.Stats, error) {
	stats := &generator.Stats{}

	// The account-trie builder is created up-front so the spec-storage
	// streaming Phase below (Phase 0) can drive per-account BeginStorage
	// sub-builders before Phase 1 even queues the account leaves.
	builder := besutrie.New(sink)

	// Reverse map for Phase 2: addrHash → original Address, so we can
	// look up cfg.GenesisAccounts[addr].Root (set by the streaming Phase
	// 0) when encoding spec-entity account leaves.
	hashToAddr := make(map[common.Hash]common.Address, len(cfg.GenesisAccounts))
	for addr := range cfg.GenesisAccounts {
		hashToAddr[crypto.Keccak256Hash(addr[:])] = addr
	}

	// --- Phase 0: stream per-spec-entity storage. ---
	//
	// For each PreAlloc entity with non-nil Storage, drain the iter
	// through streamingtrie.StorageRoot: the HashBuilder wraps a
	// per-account BeginStorage→AddSlot→Commit cycle on the besu builder
	// (writes Bonsai storage-trie nodes via sink), and the Sink writes
	// the Bonsai flat-state slot row via sink.PutFlatStorage. The
	// returned root is spliced into cfg.GenesisAccounts[addr].Root so
	// Phase 2 picks it up via hashToAddr.
	for i, pe := range cfg.PreAlloc {
		if pe.Storage == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return common.Hash{}, nil, nil, err
		}
		addr := pe.Address
		addrHash := crypto.Keccak256Hash(addr[:])
		sb := builder.BeginStorage(addrHash)
		hb := &besuStorageHashBuilder{sb: sb}
		var entityStorageBytes uint64
		streamSink := func(keyHash, _rawKey, value common.Hash) error {
			trimmed := besurlp.TrimStorageValue(value)
			entityStorageBytes += uint64(len(trimmed))
			return sink.PutFlatStorage(addrHash, keyHash, trimmed)
		}
		root, err := streamingtrie.StorageRoot("", pe.Storage, hb, streamSink)
		if err != nil {
			return common.Hash{}, nil, nil, fmt.Errorf("besu: stream spec storage[%d] %s: %w", i, addr.Hex(), err)
		}
		if acc, ok := cfg.GenesisAccounts[addr]; ok && acc != nil {
			acc.Root = root
		}
		stats.StorageBytes += entityStorageBytes
	}

	// --- Phase 1: stream entities to a shared streamsort.Store. ---

	sorter, err := streamsort.New("")
	if err != nil {
		return common.Hash{}, nil, nil, fmt.Errorf("besu: streamsort.New: %w", err)
	}
	defer sorter.Close()

	rng := mrand.New(mrand.NewSource(int64(cfg.Seed)))

	// Phase 1 target-size cap. Tracks raw entity bytes (32B addrHash key + blob)
	// and stops emission once cfg.TargetSize is reached. Over-estimates the
	// final Bonsai DB size (no trie-node overhead) but lands slightly under
	// target — preferred over going over.
	totalRawBytes := uint64(0)
	targetReached := false
	checkTarget := func(blobLen int) bool {
		totalRawBytes += uint64(32 + blobLen)
		if cfg.TargetSize > 0 && totalRawBytes >= cfg.TargetSize {
			if cfg.Verbose {
				log.Printf("besu Phase 1: raw bytes %d MiB >= target %d MiB — stopping entity emission early",
					totalRawBytes>>20, cfg.TargetSize>>20)
			}
			targetReached = true
			return true
		}
		return false
	}

	for i := 0; i < cfg.NumAccounts && !targetReached; i++ {
		if err := ctx.Err(); err != nil {
			return common.Hash{}, nil, nil, err
		}
		acc := entitygen.GenerateEOA(rng)
		addrHash := acc.AddrHash
		blob := encodeEntityEOA(acc.StateAccount.Nonce, acc.StateAccount.Balance)
		if err := sorter.Put(addrHash[:], blob); err != nil {
			return common.Hash{}, nil, nil, err
		}
		if checkTarget(len(blob)) {
			break
		}
	}

	// Genesis-alloc accounts (cfg.GenesisAccounts/GenesisCode/GenesisStorage):
	// the e2e test path uses these to deploy EIP-4788/7002/7251/2935 system
	// contracts at their canonical addresses (otherwise besu's
	// CancunPreExecutionProcessor + PraguePreExecutionProcessor reject every
	// block with "Invalid system call address"). The --spec YAML path also
	// flows here via Config.Validate's PreAlloc materialization. Geth +
	// nethermind already read these fields in their writers; besu mirrors.
	seenAlloc := make(map[common.Address]struct{}, len(cfg.GenesisAccounts))
	for addr, acc := range cfg.GenesisAccounts {
		if err := ctx.Err(); err != nil {
			return common.Hash{}, nil, nil, err
		}
		if _, dup := seenAlloc[addr]; dup {
			continue
		}
		seenAlloc[addr] = struct{}{}
		addrHash := crypto.Keccak256Hash(addr[:])
		balance := acc.Balance
		if balance == nil {
			balance = uint256.NewInt(0)
		}
		code := cfg.GenesisCode[addr]
		var blob []byte
		if len(code) == 0 && len(cfg.GenesisStorage[addr]) == 0 {
			blob = encodeEntityEOA(acc.Nonce, balance)
		} else {
			blob = encodeEntityContract(acc.Nonce, balance, code, cfg.GenesisStorage[addr])
		}
		if err := sorter.Put(addrHash[:], blob); err != nil {
			return common.Hash{}, nil, nil, err
		}
	}

	codeSize := cfg.CodeSize
	if codeSize <= 0 {
		codeSize = 1024
	}
	for i := 0; i < cfg.NumContracts && !targetReached; i++ {
		if err := ctx.Err(); err != nil {
			return common.Hash{}, nil, nil, err
		}
		// Canonical (slot-count, contract) draw order — single source of
		// truth in entitygen so every writer + every reproduction-side
		// test stays RNG-aligned.
		contract := entitygen.GenerateContractRoll(rng, cfg.Distribution, codeSize, cfg.MinSlots, cfg.MaxSlots)
		slotMap := make(map[common.Hash]common.Hash, len(contract.Storage))
		for _, s := range contract.Storage {
			slotMap[s.Key] = s.Value
		}
		addrHash := contract.AddrHash
		blob := encodeEntityContract(contract.StateAccount.Nonce, contract.StateAccount.Balance, contract.Code, slotMap)
		if err := sorter.Put(addrHash[:], blob); err != nil {
			return common.Hash{}, nil, nil, err
		}
		if checkTarget(len(blob)) {
			break
		}
	}

	// --- Phase 2: iterate sorted, drive Builder + flat-state writes. ---
	// `builder` was created up-front (line ~46) so Phase 0 could pre-
	// populate per-account storage tries for spec entities.

	if err := sorter.Iterate(func(key, value []byte) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		var addrHash common.Hash
		copy(addrHash[:], key)
		entity := decodeEntity(value)

		// Storage trie + flat slots + code.
		storageRoot := besu.EmptyTrieNodeHash
		// If Phase 0 streamed storage for this address, use the
		// pre-computed root from cfg.GenesisAccounts. Otherwise
		// compute from entity.slots below.
		if specAddr, ok := hashToAddr[addrHash]; ok {
			if acc := cfg.GenesisAccounts[specAddr]; acc != nil &&
				acc.Root != (common.Hash{}) && acc.Root != besu.EmptyTrieNodeHash {
				storageRoot = acc.Root
			}
		}
		codeHash := besu.EmptyCodeHash
		if entity.kind == entityContract {
			if len(entity.slots) > 0 {
				sb := builder.BeginStorage(addrHash)
				// Sort slots by slotHash (storage trie also requires sorted insert).
				type kv struct {
					slotHash common.Hash
					value    common.Hash
				}
				kvs := make([]kv, 0, len(entity.slots))
				for slotKey, slotVal := range entity.slots {
					kvs = append(kvs, kv{
						slotHash: crypto.Keccak256Hash(slotKey[:]),
						value:    slotVal,
					})
				}
				sort.Slice(kvs, func(i, j int) bool {
					return kvs[i].slotHash.Big().Cmp(kvs[j].slotHash.Big()) < 0
				})
				for _, e := range kvs {
					// Trie and flat-db use DIFFERENT encodings for the same
					// slot value (BonsaiWorldState.java:182-183):
					//   trie  → encodeTrieValue = RLP(trim(value))    (≤33 bytes)
					//   flat  → raw trim(value)                       (≤32 bytes)
					// Flat-db reads go through UInt256.fromBytes which
					// rejects > 32 bytes, so writing the RLP-wrapped form
					// to flat fatals at first eth_getStorageAt:
					//   "Expected at most 32 bytes but got 33"
					valueTrieRLP := besurlp.EncodeStorageValue(e.value)
					valueFlat := besurlp.TrimStorageValue(e.value)
					if err := sb.AddSlot(e.slotHash, valueTrieRLP); err != nil {
						return err
					}
					if err := sink.PutFlatStorage(addrHash, e.slotHash, valueFlat); err != nil {
						return err
					}
					stats.StorageSlotsCreated++
					stats.StorageBytes += uint64(64 + len(valueTrieRLP) + len(valueFlat))
				}
				root, err := sb.Commit()
				if err != nil {
					return err
				}
				storageRoot = root
			}
			if len(entity.code) > 0 {
				codeHash = crypto.Keccak256Hash(entity.code)
				if err := sink.PutCode(codeHash, entity.code); err != nil {
					return err
				}
				stats.CodeBytes += uint64(len(entity.code))
			}
		}

		accountRLP, err := besurlp.EncodeAccount(entity.nonce, entity.balance, storageRoot, codeHash)
		if err != nil {
			return fmt.Errorf("besu: encode account: %w", err)
		}
		if err := sink.PutFlatAccount(addrHash, accountRLP); err != nil {
			return err
		}
		if err := builder.AddAccount(addrHash, accountRLP); err != nil {
			return err
		}

		if entity.kind == entityEOA {
			stats.AccountsCreated++
		} else {
			stats.ContractsCreated++
		}
		stats.AccountBytes += uint64(32 + len(accountRLP))
		return nil
	}); err != nil {
		return common.Hash{}, nil, nil, err
	}

	// Commit the account trie. NodeSink emits remaining trie nodes.
	rootHash, rootRLP, err := builder.Commit()
	if err != nil {
		return common.Hash{}, nil, nil, fmt.Errorf("besu: trie commit: %w", err)
	}

	stats.TotalBytes = stats.AccountBytes + stats.StorageBytes + stats.CodeBytes
	return rootHash, rootRLP, stats, nil
}

// --- Entity types and encoding ---

type entityKind byte

const (
	entityEOA      entityKind = 1
	entityContract entityKind = 2
)

type entity struct {
	kind    entityKind
	nonce   uint64
	balance *uint256.Int
	code    []byte
	slots   map[common.Hash]common.Hash
}

// --- Entity blob serialization for temp Pebble ---
//
// Format (single-byte kind tag + fields):
//
//   EOA:
//     [0x01] [nonce u64 BE] [balance_len u8] [balance bytes...]
//
//   Contract:
//     [0x02] [nonce u64 BE] [balance_len u8] [balance bytes...]
//        [code_len u32 BE] [code bytes...]
//        [slot_count u32 BE] [slot_count × ([slot_key 32B] [slot_value 32B])]

func encodeEntityEOA(nonce uint64, balance *uint256.Int) []byte {
	balBytes := balance.ToBig().Bytes() // minimal big-endian
	out := make([]byte, 1+8+1+len(balBytes))
	out[0] = byte(entityEOA)
	binary.BigEndian.PutUint64(out[1:9], nonce)
	out[9] = byte(len(balBytes))
	copy(out[10:], balBytes)
	return out
}

func encodeEntityContract(nonce uint64, balance *uint256.Int, code []byte, slots map[common.Hash]common.Hash) []byte {
	balBytes := balance.ToBig().Bytes()
	size := 1 + 8 + 1 + len(balBytes) + 4 + len(code) + 4 + len(slots)*64
	out := make([]byte, 0, size)
	out = append(out, byte(entityContract))
	var nonceBuf [8]byte
	binary.BigEndian.PutUint64(nonceBuf[:], nonce)
	out = append(out, nonceBuf[:]...)
	out = append(out, byte(len(balBytes)))
	out = append(out, balBytes...)
	var codeLenBuf [4]byte
	binary.BigEndian.PutUint32(codeLenBuf[:], uint32(len(code)))
	out = append(out, codeLenBuf[:]...)
	out = append(out, code...)
	var slotCountBuf [4]byte
	binary.BigEndian.PutUint32(slotCountBuf[:], uint32(len(slots)))
	out = append(out, slotCountBuf[:]...)
	for k, v := range slots {
		out = append(out, k[:]...)
		out = append(out, v[:]...)
	}
	return out
}

func decodeEntity(blob []byte) entity {
	if len(blob) < 1 {
		panic("besu: empty entity blob")
	}
	e := entity{kind: entityKind(blob[0])}
	pos := 1
	e.nonce = binary.BigEndian.Uint64(blob[pos : pos+8])
	pos += 8
	balLen := int(blob[pos])
	pos++
	balBytes := blob[pos : pos+balLen]
	pos += balLen
	e.balance = new(uint256.Int)
	e.balance.SetBytes(balBytes)

	if e.kind == entityContract {
		codeLen := int(binary.BigEndian.Uint32(blob[pos : pos+4]))
		pos += 4
		e.code = make([]byte, codeLen)
		copy(e.code, blob[pos:pos+codeLen])
		pos += codeLen
		slotCount := int(binary.BigEndian.Uint32(blob[pos : pos+4]))
		pos += 4
		e.slots = make(map[common.Hash]common.Hash, slotCount)
		for i := 0; i < slotCount; i++ {
			var k, v common.Hash
			copy(k[:], blob[pos:pos+32])
			pos += 32
			copy(v[:], blob[pos:pos+32])
			pos += 32
			e.slots[k] = v
		}
	}
	return e
}

// besuStorageHashBuilder adapts a per-account besutrie.StorageBuilder
// to the streamingtrie.HashBuilder contract.
//
// AddLeaf calls sb.AddSlot with the streamingtrie-encoded value (trim
// leading zeros + RLP, matching besu's PathBasedWorldView.encodeTrieValue
// exactly). The Sink slot of streamingtrie is used separately by the
// per-entity adapter to write the flat-state row (raw trimmed bytes,
// per BonsaiWorldState.java:182 putStorageValueBySlotHash).
//
// Root calls sb.Commit which finalises the storage trie + emits non-
// inline nodes via the bound NodeSink.
type besuStorageHashBuilder struct {
	sb *besutrie.StorageBuilder
}

func (b *besuStorageHashBuilder) AddLeaf(keyHash common.Hash, valueRLP []byte) error {
	return b.sb.AddSlot(keyHash, valueRLP)
}

func (b *besuStorageHashBuilder) Root() (common.Hash, error) {
	return b.sb.Commit()
}
