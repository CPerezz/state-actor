//go:build cgo_neth

package nethermind

import (
	"bytes"
	"context"
	"fmt"
	"log"
	mrand "math/rand"
	"os"
	"path/filepath"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
	"github.com/linxGnu/grocksdb"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/entitygen"
	nethrlp "github.com/nerolation/state-actor/internal/neth/rlp"
	nethtrie "github.com/nerolation/state-actor/internal/neth/trie"
	"github.com/nerolation/state-actor/internal/streamingtrie"
	"github.com/nerolation/state-actor/internal/streamsort"
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
	ctx context.Context,
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

	sorter, err := streamsort.New("")
	if err != nil {
		return common.Hash{}, fmt.Errorf("open streamsort: %w", err)
	}
	defer sorter.Close()

	codeWO := grocksdb.NewDefaultWriteOptions()
	defer codeWO.Destroy()

	// Spec-entity storage streaming. Each PreAlloc entity with non-nil
	// Storage flows through a per-entity streamsort.Store → keccak-sorted
	// iterate → nethermind builder.AddStorageSlot. Bounded RAM regardless
	// of slot count, so 50 GB ERC-20 fixtures stay within budget.
	//
	// Order note: nethermind's Builder tracks "current account" via
	// FinalizeStorageRoot calls between AddStorageSlot groups. The
	// existing genesis-alloc loop below runs AddStorageSlot+Finalize in
	// random map-iteration order too, then queues AddAccount via the
	// temp DB sorter; the builder accepts this pattern (existing tests
	// cover it). The new streaming Phase preserves it: per-entity Add
	// Storage+Finalize cycle, then later AddAccount in addrHash order.
	//
	// After streaming sets the per-entity storage root we splice it into
	// cfg.GenesisAccounts[addr].Root, so the genesis-alloc loop below
	// encodes the account RLP with the correct Root (its own storage
	// block is a no-op because materializePreAlloc no longer fills
	// cfg.GenesisStorage for spec entities).
	for i, pe := range cfg.PreAlloc {
		if pe.Storage == nil {
			continue
		}
		if err := ctx.Err(); err != nil {
			return common.Hash{}, err
		}
		addr := pe.Address
		addrHash := crypto.Keccak256Hash(addr[:])
		ah := [32]byte(addrHash)
		hb := &nethermindStorageHashBuilder{builder: builder, ah: ah}
		var entityStorageBytes uint64
		statSink := func(_, _, value common.Hash) error {
			// RLP-of-trimmed-value: 1 prefix byte + len(trimmed). Matches
			// the per-slot byte tally the genesis-alloc loop performs below
			// for non-streaming slots, so reported StorageBytes is uniform
			// across spec + alloc + synthetic paths.
			v := value[:]
			for len(v) > 0 && v[0] == 0 {
				v = v[1:]
			}
			if len(v) > 0 {
				entityStorageBytes += uint64(len(v) + 1)
			}
			return nil
		}
		root, err := streamingtrie.StorageRoot("", pe.Storage, hb, statSink)
		if err != nil {
			return common.Hash{}, fmt.Errorf("nethermind: stream spec storage[%d] %s: %w", i, addr.Hex(), err)
		}
		if acc, ok := genesisAccounts[addr]; ok && acc != nil {
			acc.Root = root
		}
		if stats != nil {
			stats.StorageBytes += entityStorageBytes
		}
	}

	// Genesis-alloc accounts go to the temp DB AND the code DB AND
	// (for storage-bearing accounts) into the per-account storage-trie
	// path that produces account.Root before the account RLP is queued.
	// Storage slots flow through the standard storage-trie path:
	// keccak(slotKey)-sorted slots driven through builder.AddStorageSlot,
	// then FinalizeStorageRoot stamps account.Root.
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

		// Storage trie: slots enter the StackTrie in keccak(slotKey)-
		// ascending order; values are leading-zero-trimmed + RLP-encoded.
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
		if err := sorter.Put(ah[:], data); err != nil {
			return common.Hash{}, fmt.Errorf("queue genesis account: %w", err)
		}
		if stats != nil {
			stats.AccountBytes += uint64(len(data))
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
		if err := sorter.Put(acc.AddrHash[:], data); err != nil {
			return common.Hash{}, fmt.Errorf("queue EOA: %w", err)
		}
		if stats != nil {
			stats.AccountBytes += uint64(len(data))
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
		if err := sorter.Put(contract.AddrHash[:], data); err != nil {
			return common.Hash{}, fmt.Errorf("queue contract: %w", err)
		}
		if stats != nil {
			stats.AccountBytes += uint64(len(data))
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

	// Phase 2: addrHash-sorted iteration → AddAccount. Account-trie nodes
	// get added to the production State DB here; storage-trie + code
	// already landed in Phase 1, so Phase 2 only contributes a smaller
	// delta. cfg.TargetSize already triggered the stop in Phase 1 if we
	// were over budget — Phase 2 just finalizes the trie from whatever
	// was emitted before the stop.
	if err := sorter.Iterate(func(key, value []byte) error {
		var ah [32]byte
		copy(ah[:], key)

		var sa types.StateAccount
		if err := gethrlp.DecodeBytes(value, &sa); err != nil {
			return fmt.Errorf("decode StateAccount: %w", err)
		}

		accRLP, err := nethrlp.EncodeAccount(&sa)
		if err != nil {
			return fmt.Errorf("encode neth account: %w", err)
		}
		if err := builder.AddAccount(ah, accRLP); err != nil {
			return fmt.Errorf("add account: %w", err)
		}
		return nil
	}); err != nil {
		return common.Hash{}, fmt.Errorf("streamsort iterate: %w", err)
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

// nethermindStorageHashBuilder adapts the nethermind Builder to the
// streamingtrie.HashBuilder contract.
//
// AddLeaf calls builder.AddStorageSlot, which does BOTH the per-slot
// Storage CF row write AND the in-memory storage-trie advance. So the
// streamingtrie helper's Sink slot is left nil for nethermind — the
// builder is the single point of truth.
//
// Root calls builder.FinalizeStorageRoot, which finalises the trie for
// the bound addrHash and returns the storage root.
type nethermindStorageHashBuilder struct {
	builder *nethtrie.Builder
	ah      [32]byte
}

func (n *nethermindStorageHashBuilder) AddLeaf(keyHash common.Hash, valueRLP []byte) error {
	var kh [32]byte
	copy(kh[:], keyHash[:])
	return n.builder.AddStorageSlot(n.ah, kh, valueRLP)
}

func (n *nethermindStorageHashBuilder) Root() (common.Hash, error) {
	root, err := n.builder.FinalizeStorageRoot(n.ah)
	if err != nil {
		return common.Hash{}, err
	}
	return common.Hash(root), nil
}
