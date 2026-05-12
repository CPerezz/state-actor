//go:build cgo_neth

package nethermind

import (
	"bytes"
	"fmt"
	"log"
	mrand "math/rand"
	"os"
	"path/filepath"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
	"github.com/linxGnu/grocksdb"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/entitygen"
	nethrlp "github.com/nerolation/state-actor/internal/neth/rlp"
	nethtrie "github.com/nerolation/state-actor/internal/neth/trie"
)

// writeSyntheticAccounts generates --accounts EOAs and --contracts contracts
// via entitygen, persists their state to the State / Code DBs, and returns
// the computed state root.
//
// Pipeline:
//
//	Phase 1 (random-order generation):
//	  For each EOA / contract:
//	   - Generate via entitygen (deterministic — RNG sequence pinned by
//	     internal/entitygen golden tests).
//	   - Contracts with storage: drive Builder.AddStorageSlot per slot,
//	     then FinalizeStorageRoot. The builder's storage sink writes
//	     trie nodes to the State DB at HalfPath storage keys during these
//	     calls; the returned root is set on the contract's StateAccount.
//	   - Contract code goes to the Code DB at keccak(code).
//	   - The StateAccount (compact, ~80B) is written to a temp Pebble DB
//	     keyed by addrHash. Pebble auto-sorts on read.
//
//	Phase 2 (addrHash-sorted state-trie build):
//	  Iterate the temp Pebble DB:
//	   - Decode the stashed StateAccount.
//	   - Encode it as Nethermind RLP via internal/neth/rlp.EncodeAccount.
//	   - Call Builder.AddAccount(addrHash, accountRLP). The builder's
//	     account sink writes trie nodes to the State DB at HalfPath state
//	     keys. After all accounts: FinalizeStateRoot returns the root.
//
// Memory: O(max_slots_per_contract). Total entity count is bounded only by
// the temp Pebble DB's disk space, which streams to /tmp.
//
// genesisAccounts/genesisCodes carry --genesis alloc entries: they go into
// the same sorted account trie so the resulting state root incorporates
// both synthetic and explicitly-named accounts.
//
// stats (optional) accumulates AccountBytes (per-entity RLP-encoded
// StateAccount length), CodeBytes (raw bytecode length per contract), and
// StorageBytes (per-slot trimmed-RLP storage value length). Pass nil to
// skip accounting. Mirrors the reth/besu writers' "writer-emitted bytes"
// semantics — values differ across clients because each writer encodes
// state in its own on-disk format.
func writeSyntheticAccounts(
	dbs *nethDBs,
	cfg generator.Config,
	genesisAccounts map[common.Address]*types.StateAccount,
	genesisCodes map[common.Address][]byte,
	genesisStorages map[common.Address]map[common.Hash]common.Hash,
	stats *generator.Stats,
) (common.Hash, error) {
	sink := newStateDBSink(dbs.state)
	defer func() { _ = sink.close() }()
	builder := nethtrie.NewBuilder(sink)

	tempDir, err := os.MkdirTemp("", "neth-acct-trie-*")
	if err != nil {
		return common.Hash{}, fmt.Errorf("create temp dir: %w", err)
	}
	defer os.RemoveAll(tempDir)

	tempDB, err := pebble.New(tempDir, 128, 64, "neth-acct/", false)
	if err != nil {
		return common.Hash{}, fmt.Errorf("open temp pebble: %w", err)
	}
	defer tempDB.Close()
	batch := tempDB.NewBatch()

	const batchFlushBytes = 64 * 1024 * 1024

	flushBatchIfFull := func() error {
		if batch.ValueSize() < batchFlushBytes {
			return nil
		}
		if err := batch.Write(); err != nil {
			return fmt.Errorf("commit batch: %w", err)
		}
		batch.Reset()
		return nil
	}

	codeWO := grocksdb.NewDefaultWriteOptions()
	defer codeWO.Destroy()

	// Genesis-alloc accounts go to the temp DB AND the code DB AND
	// (for storage-bearing accounts) into the per-account storage-trie
	// path that produces account.Root before the account RLP is queued.
	//
	// Storage handling closes the nethermind half of issue #22: storage
	// slots supplied via the legacy GenesisStorage map (or materialized
	// from Config.PreAlloc by Validate's shim) are now written exactly
	// like writeGenesisAllocAccounts does it — keccak(slotKey)-sorted
	// slots driven through builder.AddStorageSlot, then FinalizeStorageRoot
	// stamping account.Root.
	for addr, acc := range genesisAccounts {
		if code, ok := genesisCodes[addr]; ok && len(code) > 0 {
			ch := crypto.Keccak256Hash(code)
			if err := dbs.code.Put(codeWO, ch[:], code); err != nil {
				return common.Hash{}, fmt.Errorf("write genesis code for %s: %w", addr.Hex(), err)
			}
			acc.CodeHash = ch[:]
			if stats != nil {
				stats.CodeBytes += uint64(len(code))
			}
		}

		// Storage trie. Lifted from writeGenesisAllocAccounts:154-213 —
		// same sort discipline (keccak(slotKey) ascending), same
		// leading-zero-trim + RLP encode, same finalize-and-stamp pattern.
		if slots := genesisStorages[addr]; len(slots) > 0 {
			ahBytes := crypto.Keccak256Hash(addr[:])
			var ah [32]byte
			copy(ah[:], ahBytes[:])

			type hashedSlot struct {
				keyHash common.Hash
				value   common.Hash
			}
			hashed := make([]hashedSlot, 0, len(slots))
			for k, v := range slots {
				hashed = append(hashed, hashedSlot{
					keyHash: crypto.Keccak256Hash(k[:]),
					value:   v,
				})
			}
			sort.Slice(hashed, func(i, j int) bool {
				return bytes.Compare(hashed[i].keyHash[:], hashed[j].keyHash[:]) < 0
			})
			for _, s := range hashed {
				v := s.value[:]
				for len(v) > 0 && v[0] == 0 {
					v = v[1:]
				}
				if len(v) == 0 {
					continue // zero slot = deletion; skip
				}
				valRLP, err := gethrlp.EncodeToBytes(v)
				if err != nil {
					return common.Hash{}, fmt.Errorf("encode genesis-alloc slot %s/%s: %w",
						addr.Hex(), s.keyHash.Hex(), err)
				}
				if err := builder.AddStorageSlot(ah, [32]byte(s.keyHash), valRLP); err != nil {
					return common.Hash{}, fmt.Errorf("add genesis-alloc storage slot %s/%s: %w",
						addr.Hex(), s.keyHash.Hex(), err)
				}
				if stats != nil {
					stats.StorageBytes += uint64(len(valRLP))
				}
			}
			storageRoot, err := builder.FinalizeStorageRoot(ah)
			if err != nil {
				return common.Hash{}, fmt.Errorf("finalize genesis-alloc storage root for %s: %w",
					addr.Hex(), err)
			}
			acc.Root = common.Hash(storageRoot)
		}

		ah := crypto.Keccak256Hash(addr[:])
		data, err := gethrlp.EncodeToBytes(acc)
		if err != nil {
			return common.Hash{}, fmt.Errorf("encode genesis account %s: %w", addr.Hex(), err)
		}
		if err := batch.Put(ah[:], data); err != nil {
			return common.Hash{}, fmt.Errorf("queue genesis account: %w", err)
		}
		if stats != nil {
			stats.AccountBytes += uint64(len(data))
		}
		if err := flushBatchIfFull(); err != nil {
			return common.Hash{}, err
		}
	}

	// Inject explicitly-requested addresses (e.g. Anvil dev account) as EOAs
	// with 999_999_999 ETH, nonce=0, no code/storage. Mirrors besu's pattern
	// at client/besu/state_writer_cgo.go and reth's at client/reth/run_cgo.go.
	// Without this, a user passing --client=nethermind --inject-accounts=0xf39F…
	// produced a chain without the requested funded address.
	injectBalance := new(uint256.Int).Mul(uint256.NewInt(999_999_999), uint256.NewInt(1_000_000_000_000_000_000))
	seenInjected := make(map[common.Address]struct{}, len(cfg.InjectAddresses))
	for _, addr := range cfg.InjectAddresses {
		if _, dup := seenInjected[addr]; dup {
			continue
		}
		seenInjected[addr] = struct{}{}
		acc := &types.StateAccount{
			Nonce:    0,
			Balance:  injectBalance,
			Root:     types.EmptyRootHash,
			CodeHash: types.EmptyCodeHash.Bytes(),
		}
		ah := crypto.Keccak256Hash(addr[:])
		data, err := gethrlp.EncodeToBytes(acc)
		if err != nil {
			return common.Hash{}, fmt.Errorf("encode injected account %s: %w", addr.Hex(), err)
		}
		if err := batch.Put(ah[:], data); err != nil {
			return common.Hash{}, fmt.Errorf("queue injected account: %w", err)
		}
		if stats != nil {
			stats.AccountBytes += uint64(len(data))
		}
		if err := flushBatchIfFull(); err != nil {
			return common.Hash{}, err
		}
	}

	// Synthetic generation. Single math/rand stream — order EOAs → contracts
	// matches the geth path's RNG draws so the state-root determinism story
	// stays consistent across clients (modulo encoding-format differences,
	// which the differential oracle catches).
	rng := mrand.New(mrand.NewSource(cfg.Seed))

	// targetReached short-circuits both Phase 1 loops AND Phase 2 below
	// when the production datadir reaches cfg.TargetSize. Nethermind's
	// Phase 1 is hybrid: account stashing goes to the temp Pebble DB, but
	// contract storage tries write to the production State DB via
	// builder.AddStorageSlot (line 211 below). So the bulk of production-
	// DB growth happens inside the contract loop here — sampling dirSize
	// only in Phase 2 (like geth does) would miss it entirely. Pattern
	// mirrors reth's streaming per-batch dirSize check.
	//
	// EOAs go straight to the temp Pebble (no production DB write), so
	// the sample fires once per EOA loop completion rather than per
	// entity — minimizes filesystem walks for the cheap case.
	targetReached := false
	checkProductionSize := func() (uint64, bool) {
		if cfg.TargetSize == 0 {
			return 0, false
		}
		if err := sink.flush(); err != nil {
			// flush failures are caught + surfaced when sink.close() is
			// called at the end of the function; silently skipping the
			// sample here is acceptable because the next iteration will
			// retry. Logging would spam.
			return 0, false
		}
		size, err := dirSize(cfg.DBPath)
		if err != nil {
			return 0, false
		}
		return size, size >= cfg.TargetSize
	}

	for i := 0; i < cfg.NumAccounts; i++ {
		acc := entitygen.GenerateEOA(rng)
		data, err := gethrlp.EncodeToBytes(acc.StateAccount)
		if err != nil {
			return common.Hash{}, fmt.Errorf("encode EOA %d: %w", i, err)
		}
		if err := batch.Put(acc.AddrHash[:], data); err != nil {
			return common.Hash{}, fmt.Errorf("queue EOA: %w", err)
		}
		if stats != nil {
			stats.AccountBytes += uint64(len(data))
		}
		if err := flushBatchIfFull(); err != nil {
			return common.Hash{}, err
		}
	}

	codeSize := cfg.CodeSize
	if codeSize <= 0 {
		codeSize = 1024
	}

	// contractSampleEvery picks the cadence of Phase 1 dirSize sampling.
	// At cfg.TargetSize=200 MiB with the canonical e2e config (5-50 slots
	// per contract, ~100B per slot of trie overhead), one batch of 100
	// contracts adds roughly 1-5 MiB of State DB nodes — fine-grained
	// enough to land within ±20% tolerance, coarse enough that filesystem
	// walks don't dominate.
	const contractSampleEvery = 100

	for i := 0; i < cfg.NumContracts && !targetReached; i++ {
		contract := entitygen.GenerateContractRoll(rng, cfg.Distribution, codeSize, cfg.MinSlots, cfg.MaxSlots)
		numSlots := len(contract.Storage)

		// Write code first — keccak(code) goes into the State leaf below.
		if err := dbs.code.Put(codeWO, contract.CodeHash[:], contract.Code); err != nil {
			return common.Hash{}, fmt.Errorf("write contract code: %w", err)
		}
		if stats != nil {
			stats.CodeBytes += uint64(len(contract.Code))
		}

		// Storage trie. AddStorageSlot expects slotKeyHash-ascending order.
		// entitygen.GenerateContract sorts by raw Key, but the trie indexes
		// by keccak(Key) — so we re-hash and re-sort here.
		if numSlots > 0 {
			slots := make([]hashedSlot, len(contract.Storage))
			for j, s := range contract.Storage {
				slots[j] = hashedSlot{
					keyHash: crypto.Keccak256Hash(s.Key[:]),
					value:   s.Value,
				}
			}
			sort.Slice(slots, func(i, j int) bool {
				return bytes.Compare(slots[i].keyHash[:], slots[j].keyHash[:]) < 0
			})

			for _, s := range slots {
				valueRLP, err := encodeStorageValueNeth(s.value)
				if err != nil {
					return common.Hash{}, fmt.Errorf("encode slot: %w", err)
				}
				if valueRLP == nil {
					// entitygen bumps zero values to 0x..01, so a nil here
					// is defensive only.
					continue
				}
				if err := builder.AddStorageSlot([32]byte(contract.AddrHash), [32]byte(s.keyHash), valueRLP); err != nil {
					return common.Hash{}, fmt.Errorf("add storage slot: %w", err)
				}
				if stats != nil {
					stats.StorageBytes += uint64(len(valueRLP))
				}
			}
			storageRoot, err := builder.FinalizeStorageRoot([32]byte(contract.AddrHash))
			if err != nil {
				return common.Hash{}, fmt.Errorf("finalize storage root: %w", err)
			}
			contract.StateAccount.Root = common.Hash(storageRoot)
		}

		data, err := gethrlp.EncodeToBytes(contract.StateAccount)
		if err != nil {
			return common.Hash{}, fmt.Errorf("encode contract %d: %w", i, err)
		}
		if err := batch.Put(contract.AddrHash[:], data); err != nil {
			return common.Hash{}, fmt.Errorf("queue contract: %w", err)
		}
		if stats != nil {
			stats.AccountBytes += uint64(len(data))
		}
		if err := flushBatchIfFull(); err != nil {
			return common.Hash{}, err
		}
		// Every contractSampleEvery contracts, flush the production
		// State DB sink and walk cfg.DBPath. Stop the loop once the
		// directory reaches cfg.TargetSize — Phase 2 below adds only
		// the account-trie nodes on top of what we've already written,
		// so landing slightly under target here leaves headroom.
		if (i+1)%contractSampleEvery == 0 {
			if size, reached := checkProductionSize(); reached {
				if cfg.Verbose {
					log.Printf("nethermind Phase 1: dirSize %d MiB >= target %d MiB after %d contracts — stopping",
						size>>20, cfg.TargetSize>>20, i+1)
				}
				targetReached = true
			}
		}
	}

	if err := batch.Write(); err != nil {
		return common.Hash{}, fmt.Errorf("final batch write: %w", err)
	}

	// Compact the temp DB so Phase 2's iterator walks fewer SSTs.
	if err := tempDB.Compact(nil, nil); err != nil {
		return common.Hash{}, fmt.Errorf("compact temp DB: %w", err)
	}

	// Phase 2: addrHash-sorted iteration → AddAccount. Account-trie nodes
	// get added to the production State DB here; storage-trie + code
	// already landed in Phase 1, so Phase 2 only contributes a smaller
	// delta. cfg.TargetSize already triggered the stop in Phase 1 if we
	// were over budget — Phase 2 just finalizes the trie from whatever
	// was emitted before the stop.
	iter := tempDB.NewIterator(nil, nil)
	defer iter.Release()

	for iter.Next() {
		var ah [32]byte
		copy(ah[:], iter.Key())

		var sa types.StateAccount
		if err := gethrlp.DecodeBytes(iter.Value(), &sa); err != nil {
			return common.Hash{}, fmt.Errorf("decode StateAccount: %w", err)
		}

		accRLP, err := nethrlp.EncodeAccount(&sa)
		if err != nil {
			return common.Hash{}, fmt.Errorf("encode neth account: %w", err)
		}
		if err := builder.AddAccount(ah, accRLP); err != nil {
			return common.Hash{}, fmt.Errorf("add account: %w", err)
		}
	}
	if err := iter.Error(); err != nil {
		return common.Hash{}, fmt.Errorf("temp DB iter: %w", err)
	}

	root, err := builder.FinalizeStateRoot()
	if err != nil {
		return common.Hash{}, fmt.Errorf("finalize state root: %w", err)
	}
	// Flush the state-trie WriteBatch before returning so the genesis-block
	// writer (which closes the State DB shortly afterward) sees a coherent
	// view, and so failures here surface synchronously.
	if err := sink.close(); err != nil {
		return common.Hash{}, fmt.Errorf("flush state writes: %w", err)
	}
	return common.Hash(root), nil
}

// encodeStorageValueNeth RLP-encodes a storage slot value with leading
// zeros trimmed — the same wire format Nethermind reads. Returns nil for
// the all-zero hash (which represents a deletion in MPT semantics).
func encodeStorageValueNeth(value common.Hash) ([]byte, error) {
	v := value[:]
	for len(v) > 0 && v[0] == 0 {
		v = v[1:]
	}
	if len(v) == 0 {
		return nil, nil
	}
	return gethrlp.EncodeToBytes(v)
}

// hashedSlot pairs a storage slot's keccak-hashed key with its value, used
// as the sort key when feeding slots into the storage-trie StackTrie.
type hashedSlot struct {
	keyHash common.Hash
	value   common.Hash
}

// dirSize returns the total bytes used by all regular files under path,
// recursively. Used by Phase 1's --target-size sampling: we walk the
// nethermind chaindata directory (which contains all 7 RocksDB subdirs
// plus the chainspec) after each contract-batch sink flush and stop the
// contract loop once it reaches the requested target. (Phase 2 only adds
// the account-trie nodes on top of what Phase 1 already wrote, so Phase 1
// is where the size budget is actually enforced.) Returns 0 + nil if path
// doesn't exist yet (rare at this point in the writer, but defensive).
// Mirrors the helper in client/geth/state_writer.go (intentionally
// duplicated per-client — matches the existing project convention).
func dirSize(path string) (uint64, error) {
	var total uint64
	err := filepath.Walk(path, func(_ string, info os.FileInfo, err error) error {
		if err != nil {
			if os.IsNotExist(err) {
				return nil
			}
			return err
		}
		if !info.IsDir() {
			total += uint64(info.Size())
		}
		return nil
	})
	return total, err
}
