//go:build cgo_reth

package reth

import (
	"bytes"
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/generator"
	iReth "github.com/nerolation/state-actor/internal/reth"
	"github.com/nerolation/state-actor/internal/streamingtrie"
)

// maxStreamSpecStorageWorkers caps the drain-phase worker count: each
// worker owns a streamsort.Store with a 64 MiB Pebble block cache and a
// MemTable that can grow to 2 GiB for huge entities. 16 workers ⇒ worst-
// case ~16 × (64 MiB + entity-dependent MemTable) of resident memory.
const maxStreamSpecStorageWorkers = 16

// streamSpecStorage writes each PreAlloc entity's Storage into reth's
// four storage tables, computes the storage MPT root, and splices it
// into cfg.GenesisAccounts[addr].Root for the alloc handler.
//
// Per-entity Mdbx.Update — not one global txn. A single Update wrapping
// the bloatnet's ~1.5B Puts degrades MDBX's dirty-page tree non-
// linearly. Drain runs in N parallel workers; the write phase
// serialises one Update per entity on the calling goroutine.
func streamSpecStorage(ctx context.Context, envs *Envs, cfg *generator.Config, stats *generator.Stats) error {
	if len(cfg.PreAlloc) == 0 {
		return nil
	}
	indices := make([]int, 0, len(cfg.PreAlloc))
	for i := range cfg.PreAlloc {
		if cfg.PreAlloc[i].Storage != nil {
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 {
		return nil
	}

	workers := runtime.NumCPU() / 2
	if workers < 1 {
		workers = 1
	}
	if workers > maxStreamSpecStorageWorkers {
		workers = maxStreamSpecStorageWorkers
	}
	if workers > len(indices) {
		workers = len(indices)
	}

	type drainedEntity struct {
		idx     int
		addr    common.Address
		drained *streamingtrie.Drained
	}

	drainCtx, cancelDrain := context.WithCancelCause(ctx)
	defer cancelDrain(nil)

	entityCh := make(chan int, workers*2)
	drainedCh := make(chan drainedEntity, workers)

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range entityCh {
				if drainCtx.Err() != nil {
					return
				}
				pe := &cfg.PreAlloc[i]
				d, err := streamingtrie.Drain(cfg.DBPath, pe.Storage)
				if err != nil {
					cancelDrain(fmt.Errorf("reth: drain spec storage[%d] %s: %w", i, pe.Address.Hex(), err))
					return
				}
				select {
				case drainedCh <- drainedEntity{idx: i, addr: pe.Address, drained: d}:
				case <-drainCtx.Done():
					d.Close()
					return
				}
			}
		}()
	}

	go func() {
		defer close(entityCh)
		for _, i := range indices {
			select {
			case entityCh <- i:
			case <-drainCtx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(drainedCh)
	}()

	var totalStorageBytes uint64
	var writeErr error
	for entry := range drainedCh {
		if writeErr != nil {
			entry.drained.Close()
			continue
		}
		addr := entry.addr
		addrHash := crypto.Keccak256Hash(addr[:])
		blockKey := iReth.BlockNumberAddress{BlockNumber: 0, Address: addr}
		var blockKeyBuf bytes.Buffer
		blockKey.EncodeKey(&blockKeyBuf)
		blockKeyBytes := blockKeyBuf.Bytes()

		var localBytes uint64
		var root common.Hash

		err := envs.Mdbx.Update(func(txn *mdbx.Txn) error {
			if err := ctx.Err(); err != nil {
				return err
			}

			sink := func(keyHash, rawKey, value common.Hash) error {
				slotValueU256 := uint256.NewInt(0).SetBytes(value[:])

				plainEntry := iReth.StorageEntry{Key: rawKey, Value: slotValueU256}
				var plainBuf bytes.Buffer
				plainEntry.EncodeCompact(&plainBuf)
				plainEntryBytes := plainBuf.Bytes()
				if err := txn.Put(envs.MdbxDBIs["PlainStorageState"], addr[:], plainEntryBytes, 0); err != nil {
					return fmt.Errorf("PlainStorageState %s slot %s: %w", addr.Hex(), rawKey.Hex(), err)
				}

				hashedEntry := iReth.StorageEntry{Key: keyHash, Value: slotValueU256}
				var hashedBuf bytes.Buffer
				hashedEntry.EncodeCompact(&hashedBuf)
				if err := txn.Put(envs.MdbxDBIs["HashedStorages"], addrHash[:], hashedBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("HashedStorages %s slot %s: %w", addrHash.Hex(), rawKey.Hex(), err)
				}

				changeEntry := iReth.StorageEntry{Key: rawKey, Value: uint256.NewInt(0)}
				var changeBuf bytes.Buffer
				changeEntry.EncodeCompact(&changeBuf)
				if err := txn.Put(envs.MdbxDBIs["StorageChangeSets"], blockKeyBytes, changeBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("StorageChangeSets %s slot %s: %w", addr.Hex(), rawKey.Hex(), err)
				}

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
				localBytes += uint64(len(plainEntryBytes))
				return nil
			}

			hb := newRethStorageHashBuilder()
			r, err := entry.drained.IterateRoot(hb, sink)
			if err != nil {
				return fmt.Errorf("reth: spec storage[%d] %s: %w", entry.idx, addr.Hex(), err)
			}
			root = r
			return nil
		})
		entry.drained.Close()
		if err != nil {
			writeErr = err
			cancelDrain(err)
			continue
		}

		if acc, ok := cfg.GenesisAccounts[addr]; ok && acc != nil {
			acc.Root = root
		}
		totalStorageBytes += localBytes
	}

	if writeErr != nil {
		return writeErr
	}
	// Surface drain-phase errors AND parent ctx cancellations. With
	// WithCancelCause, a parent ctx cancellation propagates as cause ==
	// ctx.Err() — we still want to return it rather than silently
	// reporting success on a cancelled caller.
	if cause := context.Cause(drainCtx); cause != nil {
		return cause
	}

	if stats != nil {
		stats.StorageBytes += totalStorageBytes
	}
	return nil
}

// rethStorageHashBuilder adapts iReth.HashBuilder to streamingtrie.HashBuilder.
type rethStorageHashBuilder struct {
	hb *iReth.HashBuilder
}

func newRethStorageHashBuilder() *rethStorageHashBuilder {
	return &rethStorageHashBuilder{
		hb: iReth.NewHashBuilder(func(_ iReth.StoredNibbles, _ iReth.BranchNodeCompact) error {
			return nil
		}),
	}
}

func (r *rethStorageHashBuilder) AddLeaf(keyHash common.Hash, valueRLP []byte) error {
	return r.hb.AddLeaf(addrHashToNibbles(keyHash[:]), valueRLP)
}

func (r *rethStorageHashBuilder) Root() (common.Hash, error) {
	return r.hb.Root(), nil
}
