// Package streamsort is a Pebble-backed sorted-spill store tuned for
// write-once-then-read-sorted bulk-sort workloads.
//
// The intended use is: write N (key, value) pairs in any order via
// Put → call Iterate → consume them sorted by key in a single read pass
// → Close. There is no transaction model, no crash recovery, no
// concurrent reader, and no expectation that the on-disk files survive
// the Store's lifetime — Close removes the temp directory.
//
// This is the substrate used by every client writer (geth / besu /
// nethermind / reth) for two purposes:
//
//  1. Global state-trie sort: synthesizing N accounts in batches, each
//     batch yields per-account leaves; the streamsort iterates them
//     sorted by addrHash and feeds an MPT HashBuilder.
//
//  2. Per-entity spec-storage sort: for each spec entity with a non-
//     trivial Storage iter (e.g. an ERC-20 with total_owners > 1M),
//     the writer drains the iter into a fresh Store, iterates sorted,
//     and feeds both the storage MPT builder and the per-slot DB
//     row writer in lockstep.
//
// Pebble is tuned aggressively for this workload — see New for the
// full options table; cliff notes: no WAL, huge MemTable, compaction
// deferred until iterate, no compression. The contract is "use this
// as a sorted FIFO; don't read while writing".
//
// Not concurrency-safe. One goroutine owns a Store for its entire
// lifetime.
package streamsort

import (
	"fmt"
	"math"
	"os"
	"runtime"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/sstable"
)

// memTableSize is the in-memory write buffer per Pebble MemTable.
// 2 GiB is deliberately huge: small-to-medium entities (≤ ~2 GB of
// (key, value) pairs) never flush to L0 at all — they live in a single
// sorted skiplist that's iterated directly. Larger entities flush
// progressively; the deferred-compaction setting keeps each flush
// cheap.
const memTableSize = 2 << 30

// batchFlushBytes caps the live Pebble WriteBatch size. Without this,
// the batch would grow until Put returns and we'd accumulate the
// entire input in batch memory in addition to the MemTable. 64 MiB
// matches the chunking reth's prior Sorter used.
const batchFlushBytes = 64 << 20

// blockCacheBytes is the size of Pebble's block cache. The cache is
// only meaningful for the read pass (Iterate); the small index/filter
// blocks at the top of each L0 SST are touched repeatedly during a
// k-way merge. 64 MiB is enough to keep those resident without
// allocating space we won't touch.
const blockCacheBytes = 64 << 20

// Store is a sorted-by-key spill buffer backed by a temp Pebble LSM.
//
// Lifecycle: New → Put… → Iterate → Close. Each method is single-
// goroutine. Close is idempotent; safe to defer plus call explicitly.
type Store struct {
	dir    string
	cache  *pebble.Cache
	db     *pebble.DB
	batch  *pebble.Batch
	closed bool
}

// New creates a fresh Store rooted under workDir. The caller is
// responsible for ensuring workDir has enough free disk to hold the
// sorted dataset on top of any other on-disk artefacts (Pebble spills
// to disk once memTableSize is exceeded).
//
// If workDir is empty, os.TempDir() is used (TMPDIR= override applies).
//
// Tuning knobs applied (all rationales in the package doc / plan):
//   - DisableWAL:                  true                       (no crash recovery for a temp store)
//   - MemTableSize:                2 GiB                      (small entities stay entirely in-RAM)
//   - MemTableStopWritesThreshold: 16                         (flushing MemTable never blocks Put)
//   - L0CompactionThreshold:       math.MaxInt32              (defer compaction until Iterate)
//   - L0StopWritesThreshold:       math.MaxInt32              (accept high L0 fan-out)
//   - MaxConcurrentCompactions:    runtime.NumCPU()           (parallelise lazy compactions)
//   - BytesPerSync:                0                          (disable mid-write SST fsync rate-limit)
//   - WALBytesPerSync:             0                          (belt-and-braces; WAL is off)
//   - Levels[0].Compression:       NoCompression              (random 32-byte data doesn't compress)
//   - FormatMajorVersion:          FormatNewest               (latest SSTable format)
//   - NoSyncOnClose:               true                       (temp dir; no need to fsync on close)
//   - Cache:                       64 MiB                     (block cache for the iterate phase)
//
// (DisableTableStats is a newer Pebble knob; not present in v1.1.5.
// Add when we bump the Pebble dep.)
func New(workDir string) (*Store, error) {
	dir, err := os.MkdirTemp(workDir, "streamsort-*")
	if err != nil {
		return nil, fmt.Errorf("streamsort: mkdir temp: %w", err)
	}

	cache := pebble.NewCache(blockCacheBytes)
	opts := &pebble.Options{
		DisableWAL:                  true,
		MemTableSize:                memTableSize,
		MemTableStopWritesThreshold: 16,
		L0CompactionThreshold:       math.MaxInt32,
		L0StopWritesThreshold:       math.MaxInt32,
		MaxConcurrentCompactions:    func() int { return runtime.NumCPU() },
		BytesPerSync:                0,
		WALBytesPerSync:             0,
		NoSyncOnClose:               true,
		FormatMajorVersion:          pebble.FormatNewest,
		Cache:                       cache,
		Levels: []pebble.LevelOptions{
			{Compression: sstable.NoCompression},
		},
	}

	db, err := pebble.Open(dir, opts)
	if err != nil {
		cache.Unref()
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("streamsort: open pebble at %s: %w", dir, err)
	}
	return &Store{
		dir:   dir,
		cache: cache,
		db:    db,
		batch: db.NewBatch(),
	}, nil
}

// Put inserts (key, value) into the pending batch. If the batch grows
// past batchFlushBytes it is committed to Pebble and reset.
//
// Put after Close returns an error. Put is single-goroutine.
//
// The key and value slices are copied into the batch's internal
// buffer; the caller may reuse the input slices immediately.
func (s *Store) Put(key, value []byte) error {
	if s.closed {
		return fmt.Errorf("streamsort: Put after Close")
	}
	if err := s.batch.Set(key, value, nil); err != nil {
		return fmt.Errorf("streamsort: batch.Set: %w", err)
	}
	if s.batch.Len() >= batchFlushBytes {
		if err := s.batch.Commit(pebble.NoSync); err != nil {
			return fmt.Errorf("streamsort: batch.Commit: %w", err)
		}
		s.batch.Reset()
	}
	return nil
}

// Iterate flushes any pending batch, opens a Pebble iterator over the
// full keyspace, and invokes yield(k, v) for each entry in ascending
// key order.
//
// IMPORTANT: the yield function's key/value slices alias Pebble's
// internal buffers and are invalidated by the next Next() call.
// Callers that retain either slice MUST copy it.
//
// If yield returns a non-nil error, iteration stops immediately and
// that error is returned. The iterator's own Error() takes precedence
// only on internal failure.
//
// Iterate after Close returns an error.
func (s *Store) Iterate(yield func(key, value []byte) error) error {
	if s.closed {
		return fmt.Errorf("streamsort: Iterate after Close")
	}
	if err := s.batch.Commit(pebble.NoSync); err != nil {
		return fmt.Errorf("streamsort: final batch.Commit: %w", err)
	}
	s.batch.Reset()

	iter, err := s.db.NewIter(nil)
	if err != nil {
		return fmt.Errorf("streamsort: NewIter: %w", err)
	}
	defer func() { _ = iter.Close() }()

	for iter.First(); iter.Valid(); iter.Next() {
		if err := yield(iter.Key(), iter.Value()); err != nil {
			return err
		}
	}
	if err := iter.Error(); err != nil {
		return fmt.Errorf("streamsort: iter.Error: %w", err)
	}
	return nil
}

// Close commits any pending batch (best-effort), closes the Pebble DB,
// frees the block cache, and removes the on-disk temp directory.
// Idempotent — subsequent calls return nil. Temp-dir RemoveAll errors
// are logged-implicit (return nil) since the underlying generation
// has already succeeded by the time Close is called; leftover temp
// space is hygiene, not correctness.
func (s *Store) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true

	var firstErr error

	// Best-effort flush of any pending writes. If this fails we still
	// proceed to Close + cleanup so resources are released.
	if err := s.batch.Commit(pebble.NoSync); err != nil {
		firstErr = fmt.Errorf("streamsort: final batch.Commit: %w", err)
	}
	s.batch.Reset()

	if err := s.db.Close(); err != nil && firstErr == nil {
		firstErr = fmt.Errorf("streamsort: db.Close: %w", err)
	}
	if s.cache != nil {
		s.cache.Unref()
	}
	_ = os.RemoveAll(s.dir) // leftover temp dir is hygiene, not correctness
	return firstErr
}
