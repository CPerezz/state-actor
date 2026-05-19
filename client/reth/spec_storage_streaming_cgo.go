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

// maxStreamSpecStorageWorkers caps the drain-and-compute worker count.
// Matches geth's maxPhase0Workers — both clients use the same upper
// bound. Each worker owns a streamsort.Store; at 8 workers RAM stays
// under the 32 GiB target after the streamsort memtable downsizing
// (commit aa0bfcb).
const maxStreamSpecStorageWorkers = 8

// chunkSlots is the slot-batch size workers emit to the consumer.
// Bounds in-flight memory per entity: chunkSlots × ~250 B/slot ×
// chunkChanCap = ~1 MiB peak across all 8 workers. Chunking lets a
// worker make progress on IterateRoot while the consumer drains
// earlier chunks.
const chunkSlots = 1024

// chunkChanCap is the per-entity chunk-channel buffer depth. With
// cap=4, the worker can produce 4 chunks ahead before blocking on the
// consumer — enough to hide the consumer's per-Mdbx.Update commit
// latency without unbounded RAM growth.
const chunkChanCap = 4

// slotPrepared holds the four pre-encoded byte buffers a single slot
// contributes to MDBX. Produced on the worker goroutine, consumed on
// the main goroutine inside Mdbx.Update — no encoding work in the
// hot write path.
type slotPrepared struct {
	plainEntry  []byte // PlainStorageState value (encoded rawKey || value)
	hashedEntry []byte // HashedStorages value (encoded keyHash || value)
	changeEntry []byte // StorageChangeSets value (encoded rawKey || 0); nil in full mode
	sskKey      []byte // StoragesHistory key (StorageShardedKey-encoded); nil in full mode
}

// preparedEntity carries everything the consumer needs to commit one
// entity's storage to MDBX. The worker fills chunkCh in keccak-
// ascending order, then sends the computed root via rootCh and closes
// chunkCh. errCh carries any IterateRoot failure surfacing on the
// worker side. trieRows are pre-encoded StoragesTrie DupSort values
// (each entry = SubKey(33 bytes) || BranchNodeCompact bytes); the
// consumer writes them under the DupSort main key ent.addrHash after
// the per-slot rows land. Emitted in path-lex order so cursor.AppendDup
// takes the fast path.
type preparedEntity struct {
	idx        int
	addr       common.Address
	addrHash   common.Hash
	blockKey   []byte         // BlockNumberAddress-encoded
	historyVal []byte         // EncodeIntegerList([0]) — fixed per entity for genesis pre-state
	chunkCh    chan []slotPrepared
	rootCh     chan common.Hash
	errCh      chan error
	trieRows   [][]byte
}

// streamSpecStorage writes each PreAlloc entity's Storage into reth's
// four storage tables, computes the storage MPT root, and splices it
// into cfg.GenesisAccounts[addr].Root for the alloc handler.
//
// Architecture: N parallel workers each (a) drain their entity's iter
// into a per-call streamsort.Store, (b) walk the sorted store driving
// the HashBuilder and pre-encoding all 4 per-slot byte buffers into
// chunks. The single consumer goroutine opens one Mdbx.Update per
// entity and does just the 4 txn.Put calls per slot — the encoding,
// keccak, and HashBuilder work happens on the workers.
//
// Per-entity Mdbx.Update — not one global txn — because a single
// global txn wrapping ~1.5B Puts degrades MDBX's dirty-page tree non-
// linearly. MDBX enforces single-writer-per-env so the commits run
// serially anyway; the win is offloading every cycle of CPU work to
// the workers so the consumer is pure I/O.
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

	workers := runtime.NumCPU()
	if workers < 1 {
		workers = 1
	}
	if workers > maxStreamSpecStorageWorkers {
		workers = maxStreamSpecStorageWorkers
	}
	if workers > len(indices) {
		workers = len(indices)
	}

	drainCtx, cancelDrain := context.WithCancelCause(ctx)
	defer cancelDrain(nil)

	entityCh := make(chan int, workers*2)
	// drainedCh is unbuffered — the consumer applies back-pressure on
	// the workers (only one entity in flight on the writer side beyond
	// the workers' local chunk buffers). Buffer of workers*2 to absorb
	// small latency spikes.
	preparedCh := make(chan *preparedEntity, workers*2)

	// EncodeIntegerList([0]) is identical for every slot in a genesis
	// pre-state import — encode once on the caller goroutine, reuse
	// across all workers.
	var sharedHistoryBuf bytes.Buffer
	iReth.EncodeIntegerList(&sharedHistoryBuf, []uint64{0})
	historyVal := sharedHistoryBuf.Bytes()

	var wg sync.WaitGroup
	for w := 0; w < workers; w++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			for i := range entityCh {
				if drainCtx.Err() != nil {
					return
				}
				if err := drainAndEncodeEntity(drainCtx, cfg, i, historyVal, preparedCh); err != nil {
					cancelDrain(err)
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
		close(preparedCh)
	}()

	var totalStorageBytes uint64
	var writeErr error
	for ent := range preparedCh {
		if writeErr != nil {
			// Drain remaining chunks so the worker doesn't block on send.
			for range ent.chunkCh {
			}
			continue
		}
		bytesWritten, err := consumeEntity(ctx, envs, cfg, ent)
		if err != nil {
			writeErr = err
			cancelDrain(err)
			continue
		}
		if acc, ok := cfg.GenesisAccounts[ent.addr]; ok && acc != nil {
			// Wait for the worker's computed root. By the time chunkCh
			// closed (consumeEntity returns), exactly one of {rootCh,
			// errCh} has a value.
			select {
			case root := <-ent.rootCh:
				acc.Root = root
			case err := <-ent.errCh:
				writeErr = err
				cancelDrain(err)
				continue
			case <-ctx.Done():
				return ctx.Err()
			}
		}
		totalStorageBytes += bytesWritten
	}

	if writeErr != nil {
		return writeErr
	}
	if cause := context.Cause(drainCtx); cause != nil {
		return cause
	}

	if stats != nil {
		stats.StorageBytes += totalStorageBytes
	}
	return nil
}

// drainAndEncodeEntity runs on a worker goroutine. It drains the
// entity's storage iter into a streamsort, then walks the sorted store
// driving the HashBuilder AND pre-encoding all 4 per-slot byte buffers
// into chunks sent to the consumer via ent.chunkCh.
func drainAndEncodeEntity(
	drainCtx context.Context,
	cfg *generator.Config,
	idx int,
	historyVal []byte,
	preparedCh chan<- *preparedEntity,
) error {
	pe := &cfg.PreAlloc[idx]
	addr := pe.Address
	addrHash := crypto.Keccak256Hash(addr[:])

	d, err := streamingtrie.Drain(cfg.DBPath, pe.Storage)
	if err != nil {
		return fmt.Errorf("reth: drain spec storage[%d] %s: %w", idx, addr.Hex(), err)
	}

	blockKey := iReth.BlockNumberAddress{BlockNumber: 0, Address: addr}
	var blockKeyBuf bytes.Buffer
	blockKey.EncodeKey(&blockKeyBuf)

	ent := &preparedEntity{
		idx:        idx,
		addr:       addr,
		addrHash:   addrHash,
		blockKey:   blockKeyBuf.Bytes(),
		historyVal: historyVal,
		chunkCh:    make(chan []slotPrepared, chunkChanCap),
		rootCh:     make(chan common.Hash, 1),
		errCh:      make(chan error, 1),
	}

	// Hand the preparedEntity to the consumer BEFORE starting iteration
	// so the consumer can begin draining chunks as soon as the first one
	// is ready. The consumer is responsible for closing the Drained
	// indirectly by waiting for chunkCh to close.
	select {
	case preparedCh <- ent:
	case <-drainCtx.Done():
		d.Close()
		return drainCtx.Err()
	}

	archive := cfg.Archive

	// Per-entity trie emissions: HashBuilder emits in path-lex order as
	// it walks the sorted slots. Worker pre-encodes each emission into a
	// StorageTrieEntry value (SubKey||BNC) and accumulates into
	// trieRows. The consumer drains trieRows into StoragesTrie within
	// the same per-entity Mdbx.Update txn AFTER the slot rows land —
	// keeping the cursor.AppendDup fast path intact for both sides.
	// Memory per entity: O(trie nodes) ≈ small multiple of slot count.
	trieRows := make([][]byte, 0, 64)
	emit := func(path iReth.StoredNibbles, node iReth.BranchNodeCompact) error {
		var valBuf bytes.Buffer
		entry := iReth.StorageTrieEntry{SubKey: path, Node: node}
		entry.EncodeCompact(&valBuf)
		trieRows = append(trieRows, valBuf.Bytes())
		return nil
	}
	hb := newRethStorageHashBuilderWithEmit(emit)
	pending := make([]slotPrepared, 0, chunkSlots)

	flushPending := func() error {
		if len(pending) == 0 {
			return nil
		}
		select {
		case ent.chunkCh <- pending:
			pending = make([]slotPrepared, 0, chunkSlots)
			return nil
		case <-drainCtx.Done():
			return drainCtx.Err()
		}
	}

	sink := func(keyHash, rawKey, value common.Hash) error {
		slotValueU256 := uint256.NewInt(0).SetBytes(value[:])

		prep := slotPrepared{}

		plainEntry := iReth.StorageEntry{Key: rawKey, Value: slotValueU256}
		var plainBuf bytes.Buffer
		plainEntry.EncodeCompact(&plainBuf)
		prep.plainEntry = plainBuf.Bytes()

		hashedEntry := iReth.StorageEntry{Key: keyHash, Value: slotValueU256}
		var hashedBuf bytes.Buffer
		hashedEntry.EncodeCompact(&hashedBuf)
		prep.hashedEntry = hashedBuf.Bytes()

		if archive {
			changeEntry := iReth.StorageEntry{Key: rawKey, Value: uint256.NewInt(0)}
			var changeBuf bytes.Buffer
			changeEntry.EncodeCompact(&changeBuf)
			prep.changeEntry = changeBuf.Bytes()

			// StoragesHistory key is only built in archive mode; in full
			// mode the consumer skips the Put entirely.
			ssk := iReth.StorageShardedKey{
				Address:     addr,
				StorageKey:  rawKey,
				BlockNumber: ^uint64(0),
			}
			var sskBuf bytes.Buffer
			ssk.EncodeKey(&sskBuf)
			prep.sskKey = sskBuf.Bytes()
		}

		pending = append(pending, prep)
		if len(pending) >= chunkSlots {
			return flushPending()
		}
		return nil
	}

	root, err := d.IterateRoot(hb, sink)
	d.Close()
	if err != nil {
		ent.errCh <- fmt.Errorf("reth: iterate spec storage[%d] %s: %w", idx, addr.Hex(), err)
		close(ent.chunkCh)
		return nil
	}

	// Flush trailing chunk (if any) before closing the channel.
	if err := flushPending(); err != nil {
		ent.errCh <- err
		close(ent.chunkCh)
		return nil
	}
	// Stash trie emissions on the preparedEntity BEFORE closing chunkCh
	// so the consumer sees them when the for-range loop exits.
	ent.trieRows = trieRows
	close(ent.chunkCh)
	ent.rootCh <- root
	return nil
}

// consumeEntity runs on the main goroutine. Opens one Mdbx.Update per
// entity and drains the worker's chunks into 4 txn.Put calls per slot.
// All bytes are pre-encoded by the worker; this function does pure I/O.
func consumeEntity(
	ctx context.Context,
	envs *Envs,
	cfg *generator.Config,
	ent *preparedEntity,
) (uint64, error) {
	var localBytes uint64
	err := envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		if err := ctx.Err(); err != nil {
			return err
		}
		for chunk := range ent.chunkCh {
			for _, prep := range chunk {
				if err := txn.Put(envs.MdbxDBIs["PlainStorageState"], ent.addr[:], prep.plainEntry, 0); err != nil {
					return fmt.Errorf("PlainStorageState %s: %w", ent.addr.Hex(), err)
				}
				if err := txn.Put(envs.MdbxDBIs["HashedStorages"], ent.addrHash[:], prep.hashedEntry, 0); err != nil {
					return fmt.Errorf("HashedStorages %s: %w", ent.addrHash.Hex(), err)
				}
				if prep.changeEntry != nil {
					// Archive mode: write the StorageChangeSets row.
					if err := txn.Put(envs.MdbxDBIs["StorageChangeSets"], ent.blockKey, prep.changeEntry, 0); err != nil {
						return fmt.Errorf("StorageChangeSets %s: %w", ent.addr.Hex(), err)
					}
				}
				if prep.sskKey != nil {
					// Archive mode: write the StoragesHistory row.
					if err := txn.Put(envs.MdbxDBIs["StoragesHistory"], prep.sskKey, ent.historyVal, 0); err != nil {
						return fmt.Errorf("StoragesHistory %s: %w", ent.addr.Hex(), err)
					}
				}
				localBytes += uint64(len(prep.plainEntry))
			}
		}
		// Drain pre-encoded trie rows into StoragesTrie under the
		// DupSort main key keccak(address). Worker emitted them in
		// path-lex order; cursor.AppendDup writes sequentially without
		// the B-tree-locate cost. Without these rows reth's payload
		// builder falls back to a linear HashedStorages walk per block.
		if len(ent.trieRows) > 0 {
			cur, cerr := txn.OpenCursor(envs.MdbxDBIs["StoragesTrie"])
			if cerr != nil {
				return fmt.Errorf("open StoragesTrie cursor %s: %w", ent.addr.Hex(), cerr)
			}
			for _, row := range ent.trieRows {
				if err := cur.Put(ent.addrHash[:], row, mdbx.AppendDup); err != nil {
					cur.Close()
					return fmt.Errorf("StoragesTrie %s: %w", ent.addr.Hex(), err)
				}
			}
			cur.Close()
		}
		return nil
	})
	if err != nil {
		return 0, err
	}
	return localBytes, nil
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

// newRethStorageHashBuilderWithEmit returns a HashBuilder configured for
// full-emissions mode: every branch with RLP ≥ 32 bytes triggers emit.
// state-actor's spec-storage workers use this so each per-entity storage
// trie populates StoragesTrie alongside the per-slot data rows.
func newRethStorageHashBuilderWithEmit(emit iReth.NodeEmitter) *rethStorageHashBuilder {
	return &rethStorageHashBuilder{
		hb: iReth.NewHashBuilderFullEmissions(emit),
	}
}

func (r *rethStorageHashBuilder) AddLeaf(keyHash common.Hash, valueRLP []byte) error {
	return r.hb.AddLeaf(addrHashToNibbles(keyHash[:]), valueRLP)
}

func (r *rethStorageHashBuilder) Root() (common.Hash, error) {
	return r.hb.Root(), nil
}
