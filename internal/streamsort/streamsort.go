// Package streamsort is a Pebble-backed sorted-spill store: write N
// (key, value) pairs via Put, call Iterate to consume them sorted in a
// single pass, Close to release.
//
// No transaction model, no crash recovery, no concurrent reader. Close
// removes the temp directory. Not concurrency-safe — one goroutine owns
// a Store for its lifetime.
package streamsort

import (
	"fmt"
	"log"
	"math"
	"os"
	"runtime"

	"github.com/cockroachdb/pebble"
	"github.com/cockroachdb/pebble/sstable"
)

// MemTableSize is the in-memory write buffer per Pebble MemTable.
// Datasets up to this size stay entirely in RAM (no L0 flush).
//
// 256 MiB chosen for the parallel-drain case: with maxPhase0Workers=8
// concurrent streamsort.Stores, the prior 2 GiB cap × (1 active + 16
// immutable memtables) reached 272 GiB worst-case RAM — directly caused
// the cap=32 OOM-kill at 127 GiB on the 125 GiB bench box. 256 MiB
// keeps per-Store peak under ~520 MiB (memtable + cache); 8 workers
// total ~4 GiB. More frequent L0 flushes are absorbed by Pebble's
// merging iterator (~5-15% per-Store throughput cost, run in parallel).
const MemTableSize = 256 << 20

const (
	batchFlushBytes = 64 << 20
	// blockCacheBytes is the per-Store Pebble block cache. Small because
	// streamsort.Iterate is a single sequential scan — the cache benefit
	// is tiny vs the per-worker RAM cost at 8 parallel Stores.
	blockCacheBytes = 8 << 20
)

// Store is a sorted-by-key spill buffer backed by a temp Pebble LSM.
// Lifecycle: New → Put… → Iterate → Close. Close is idempotent.
type Store struct {
	dir    string
	cache  *pebble.Cache
	db     *pebble.DB
	batch  *pebble.Batch
	closed bool
}

// New creates a Store rooted under workDir (empty → os.TempDir()).
// Caller is responsible for sufficient free disk to hold the spilled
// dataset.
func New(workDir string) (*Store, error) {
	dir, err := os.MkdirTemp(workDir, "streamsort-*")
	if err != nil {
		return nil, fmt.Errorf("streamsort: mkdir temp: %w", err)
	}

	cache := pebble.NewCache(blockCacheBytes)
	opts := &pebble.Options{
		DisableWAL:                  true,
		MemTableSize:                MemTableSize,
		MemTableStopWritesThreshold: 2,
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

// Put inserts (key, value) into the pending batch, flushing when the
// batch exceeds batchFlushBytes. Key and value are copied; the caller
// may reuse the input slices. Returns an error if called after Close.
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

// Iterate flushes any pending batch and invokes yield for every entry
// in ascending key order. Safe to call multiple times — each call
// re-opens a fresh Pebble iter over the same data. Key/value slices
// alias Pebble's internal buffers and are invalidated on the next
// Next() — callers that retain either MUST copy it. A non-nil error
// from yield short-circuits and is returned. Returns an error if
// called after Close.
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

// Close flushes any pending batch, closes the DB, frees the cache, and
// removes the temp directory. Idempotent. RemoveAll failures are
// logged, not returned.
func (s *Store) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true

	var firstErr error
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
	if err := os.RemoveAll(s.dir); err != nil {
		log.Printf("streamsort: cleanup of %s failed: %v", s.dir, err)
	}
	return firstErr
}
