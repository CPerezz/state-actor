//go:build cgo_reth

package reth

import (
	"bytes"
	"context"
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/generator"
	iReth "github.com/nerolation/state-actor/internal/reth"
	"github.com/nerolation/state-actor/internal/streamingtrie"
)

// streamSpecStorage drains every PreAlloc entity's Storage iter through
// streamingtrie.StorageRoot — bounded RAM regardless of slot count.
// For each entity with non-empty storage it:
//
//  1. Writes the per-slot rows across the four storage tables
//     (PlainStorageState, HashedStorages, StorageChangeSets,
//     StoragesHistory) via the Sink, in sorted-by-keccak(keyHash) order.
//
//  2. Computes the canonical storage MPT root.
//
//  3. Splices that root into cfg.GenesisAccounts[entity.Address].Root
//     so the subsequent Phase 4a.5 alloc handler sees the correct value.
//
// This runs BEFORE Phase 4a.5. The alloc handler's `WriteContracts`
// has been updated to preserve `Root` when `len(Storage) == 0`, so the
// pre-set root carries through to the account leaf and the global
// state trie.
//
// Stats are accumulated in a local variable and transferred to the
// caller's *generator.Stats only after Mdbx.Update commits cleanly —
// mirroring WriteContracts. A rolled-back commit therefore leaves
// stats untouched (no phantom byte counts for writes that never
// landed).
func streamSpecStorage(ctx context.Context, envs *Envs, cfg *generator.Config, stats *generator.Stats) error {
	if len(cfg.PreAlloc) == 0 {
		return nil
	}
	var totalStorageBytes uint64
	err := envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		var localStorageBytes uint64
		for i, pe := range cfg.PreAlloc {
			if pe.Storage == nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			addr := pe.Address
			addrHash := crypto.Keccak256Hash(addr[:])
			blockKey := iReth.BlockNumberAddress{BlockNumber: 0, Address: addr}
			var blockKeyBuf bytes.Buffer
			blockKey.EncodeKey(&blockKeyBuf)
			blockKeyBytes := blockKeyBuf.Bytes()

			// Sink writes the 4 storage tables per slot, in keccak-sorted
			// order. Each call is exactly one slot.
			sink := func(keyHash, rawKey, value common.Hash) error {
				slotValueU256 := uint256.NewInt(0).SetBytes(value[:])

				// 1. PlainStorageState: addr → StorageEntry{rawKey, value}
				plainEntry := iReth.StorageEntry{Key: rawKey, Value: slotValueU256}
				var plainBuf bytes.Buffer
				plainEntry.EncodeCompact(&plainBuf)
				plainEntryBytes := plainBuf.Bytes()
				if err := txn.Put(envs.MdbxDBIs["PlainStorageState"], addr[:], plainEntryBytes, 0); err != nil {
					return fmt.Errorf("PlainStorageState %s slot %s: %w", addr.Hex(), rawKey.Hex(), err)
				}

				// 2. HashedStorages: keccak(addr) → StorageEntry{keyHash, value}
				hashedEntry := iReth.StorageEntry{Key: keyHash, Value: slotValueU256}
				var hashedBuf bytes.Buffer
				hashedEntry.EncodeCompact(&hashedBuf)
				if err := txn.Put(envs.MdbxDBIs["HashedStorages"], addrHash[:], hashedBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("HashedStorages %s slot %s: %w", addrHash.Hex(), rawKey.Hex(), err)
				}

				// 3. StorageChangeSets: BlockNumberAddress → StorageEntry{rawKey, 0}
				changeEntry := iReth.StorageEntry{Key: rawKey, Value: uint256.NewInt(0)}
				var changeBuf bytes.Buffer
				changeEntry.EncodeCompact(&changeBuf)
				if err := txn.Put(envs.MdbxDBIs["StorageChangeSets"], blockKeyBytes, changeBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("StorageChangeSets %s slot %s: %w", addr.Hex(), rawKey.Hex(), err)
				}

				// 4. StoragesHistory: StorageShardedKey{addr, rawKey, MAX} → IntegerList([0])
				ssk := iReth.StorageShardedKey{
					Address:     addr,
					StorageKey:  rawKey,
					BlockNumber: ^uint64(0),
				}
				var sskBuf bytes.Buffer
				ssk.EncodeKey(&sskBuf)
				var listBuf bytes.Buffer
				iReth.EncodeIntegerList(&listBuf, []uint64{0})
				if err := txn.Put(envs.MdbxDBIs["StoragesHistory"], sskBuf.Bytes(), listBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("StoragesHistory %s slot %s: %w", addr.Hex(), rawKey.Hex(), err)
				}
				localStorageBytes += uint64(len(plainEntryBytes))
				return nil
			}

			hb := newRethStorageHashBuilder()
			root, err := streamingtrie.StorageRoot(cfg.DBPath, pe.Storage, hb, sink)
			if err != nil {
				return fmt.Errorf("streamSpecStorage[%d] %s: %w", i, addr.Hex(), err)
			}

			// Splice the computed root into the materialized Account so the
			// subsequent Phase 4a.5 alloc handler writes the correct account
			// leaf into the global state trie.
			if acc, ok := cfg.GenesisAccounts[addr]; ok && acc != nil {
				acc.Root = root
			}
		}
		totalStorageBytes = localStorageBytes
		return nil
	})
	if err != nil {
		return err
	}
	if stats != nil {
		stats.StorageBytes += totalStorageBytes
	}
	return nil
}

// rethStorageHashBuilder adapts iReth.HashBuilder to the
// streamingtrie.HashBuilder contract: AddLeaf(keyHash, valueRLP) →
// unpack keyHash to nibbles, then call the underlying builder.
type rethStorageHashBuilder struct {
	hb *iReth.HashBuilder
}

func newRethStorageHashBuilder() *rethStorageHashBuilder {
	return &rethStorageHashBuilder{
		hb: iReth.NewHashBuilder(func(_ iReth.StoredNibbles, _ iReth.BranchNodeCompact) error {
			return nil // storage-trie branch nodes aren't persisted at genesis
		}),
	}
}

func (r *rethStorageHashBuilder) AddLeaf(keyHash common.Hash, valueRLP []byte) error {
	return r.hb.AddLeaf(addrHashToNibbles(keyHash[:]), valueRLP)
}

func (r *rethStorageHashBuilder) Root() (common.Hash, error) {
	return r.hb.Root(), nil
}
