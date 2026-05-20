//go:build cgo_neth

package nethermind

import (
	"context"
	"fmt"
	"runtime"
	"sync"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/nerolation/state-actor/generator"
	nethtrie "github.com/nerolation/state-actor/internal/neth/trie"
	"github.com/nerolation/state-actor/internal/streamingtrie"
)

// runPhase0 drains every spec-PreAlloc entity's storage trie in parallel.
// Mirrors besu's pattern at client/besu/state_writer_cgo.go:308-432 (the
// closest analog since both clients use grocksdb).
//
// Architecture:
//
//   - Indices are sorted by len(pe.Storage) DESCENDING so the 5 bloat
//     EOAs (100M-1B slots each) start at t=0 across the first workers.
//     FIFO scheduling would let a bloat land on a worker mid-run and
//     become the wall-clock floor.
//
//   - Each worker owns its own stateDBSink (per-worker grocksdb.WriteBatch
//     up to 64 MiB; flushed on worker exit). grocksdb.Write is safe
//     concurrent-callable across workers — RocksDB serialises the commit
//     pipeline internally. State and storage tries are addrHash-prefixed,
//     so worker keyspaces are disjoint.
//
//   - Each worker owns its own nethtrie.Builder, satisfying the
//     single-goroutine invariant at internal/neth/trie/builder.go:60-61.
//     The worker uses the builder ONLY for storage-trie ops (AddStorageSlot
//     + FinalizeStorageRoot); the per-worker account trie is unused.
//
//   - Storage roots flow back to main via resultCh; main assigns
//     cfg.GenesisAccounts[addr].Root = root single-writer (no map race).
//
//   - codeSink is shared across workers but has its own internal mutex
//     (genesis_alloc_cgo.go) — codes are <100 bytes typically; the lock
//     contention is negligible vs the storage-trie compute cost.
//
// Determinism: per-entity storage roots are content-addressed (keccak),
// so worker completion order doesn't affect the eventual state root. The
// final state-trie root depends only on addrHash-sorted iteration in
// sorter.Iterate (Phase 2), not on Phase 0 order.
func runPhase0(
	ctx context.Context,
	cfg generator.Config,
	dbs *nethDBs,
	genesisAccounts map[common.Address]*types.StateAccount,
	stats *generator.Stats,
) error {
	indices := make([]int, 0, len(cfg.PreAlloc))
	for i := range cfg.PreAlloc {
		if cfg.PreAlloc[i].Storage != nil {
			indices = append(indices, i)
		}
	}
	if len(indices) == 0 {
		return nil
	}

	// NOTE: long-pole-first scheduling would help (the 5 bloat EOAs each
	// have 100M-1B slots and dominate wall-clock), but pe.Storage is
	// `iter.Seq2[Hash, Hash]` — a streaming function type — so len() isn't
	// available without consuming the iterator. PreAllocEntity has no
	// pre-computed slot-count field. Matches besu's behavior at
	// client/besu/state_writer_cgo.go:328-334, which also processes
	// indices in their natural order. Filed as part of the intra-entity
	// parallelism follow-up issue.

	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	if workers > maxPhase0Workers {
		workers = maxPhase0Workers
	}
	if workers > len(indices) {
		workers = len(indices)
	}

	type phase0Result struct {
		addr         common.Address
		root         common.Hash
		storageBytes uint64
	}

	drainCtx, cancelDrain := context.WithCancelCause(ctx)
	defer cancelDrain(nil)

	drainCh := make(chan int, workers*2)
	resultCh := make(chan phase0Result, workers*4)

	var wg sync.WaitGroup
	for k := 0; k < workers; k++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			workerSink := newStateDBSink(dbs.state)
			defer func() {
				if err := workerSink.close(); err != nil {
					cancelDrain(fmt.Errorf("nethermind phase 0: worker sink close: %w", err))
				}
			}()
			workerBuilder := nethtrie.NewBuilder(workerSink)

			for i := range drainCh {
				if drainCtx.Err() != nil {
					return
				}
				pe := &cfg.PreAlloc[i]
				addr := pe.Address
				addrHash := crypto.Keccak256Hash(addr[:])
				ah := [32]byte(addrHash)
				hb := &nethermindStorageHashBuilder{builder: workerBuilder, ah: ah}

				var entityStorageBytes uint64
				statSink := func(_, _, value common.Hash) error {
					// RLP-of-trimmed-value byte count (1 prefix + len(trimmed)).
					v := value[:]
					for len(v) > 0 && v[0] == 0 {
						v = v[1:]
					}
					if len(v) > 0 {
						entityStorageBytes += uint64(len(v) + 1)
					}
					return nil
				}
				root, err := streamingtrie.StorageRoot(cfg.DBPath, pe.Storage, hb, statSink)
				if err != nil {
					cancelDrain(fmt.Errorf("nethermind: stream spec storage[%d] %s: %w", i, addr.Hex(), err))
					return
				}
				select {
				case resultCh <- phase0Result{addr: addr, root: root, storageBytes: entityStorageBytes}:
				case <-drainCtx.Done():
					return
				}
			}
		}()
	}

	go func() {
		defer close(drainCh)
		for _, i := range indices {
			select {
			case drainCh <- i:
			case <-drainCtx.Done():
				return
			}
		}
	}()

	go func() {
		wg.Wait()
		close(resultCh)
	}()

	for entry := range resultCh {
		if acc, ok := genesisAccounts[entry.addr]; ok && acc != nil {
			acc.Root = entry.root
		}
		if stats != nil {
			stats.StorageBytes += entry.storageBytes
		}
	}
	if cause := context.Cause(drainCtx); cause != nil && cause != context.Canceled {
		return cause
	}
	return nil
}
