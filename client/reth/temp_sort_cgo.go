//go:build cgo_reth

package reth

import (
	"fmt"
	"log"
	"os"

	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
)

// sorterFlushBytes is the live WriteBatch payload size at which the sorter
// flushes to disk. Matches generator/generator.go:828 — keeps sustained RAM
// for the streaming sorter bounded regardless of total payload.
const sorterFlushBytes = 64 * 1024 * 1024

// Sorter is a Pebble-backed key-sorted spill buffer used by the streaming
// Phase 4 in RunCgo. The contract: Put any number of (key, value) pairs in
// any order; Iterate yields them back sorted by key. Pebble's LSM does the
// sort transparently on read.
//
// Memory bound: O(sorterFlushBytes + Pebble's own caches) regardless of total
// data volume. The sorter is *not* concurrency-safe — single goroutine only.
//
// Lifecycle: NewSorter → Put… → Iterate → Close. Close is idempotent and
// removes the on-disk temp directory; safe to defer plus call explicitly.
type Sorter struct {
	dir    string
	db     ethdb.KeyValueStore
	batch  ethdb.Batch
	closed bool
}

// NewSorter creates a fresh Pebble-backed sorter rooted under workDir. If
// workDir is empty, os.TempDir() is used (TMPDIR= override applies). The
// caller is responsible for ensuring workDir has enough free disk to hold
// the entire sorted dataset (typically 1–2 GB per 5M EOAs in this project).
func NewSorter(workDir string) (*Sorter, error) {
	dir, err := os.MkdirTemp(workDir, "reth-sort-*")
	if err != nil {
		return nil, fmt.Errorf("Sorter: mkdir temp: %w", err)
	}
	// Args mirror generator/generator.go:806 (cache=128MB, handles=64) — these
	// are Pebble defaults that have proven adequate for the geth bintrie
	// streaming builder at multi-million-entry scale.
	db, err := pebble.New(dir, 128, 64, "reth-sort/", false)
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("Sorter: open pebble: %w", err)
	}
	return &Sorter{dir: dir, db: db, batch: db.NewBatch()}, nil
}

// Put inserts (key, value) into the pending batch. If the batch grows past
// sorterFlushBytes it is written to disk and reset.
func (s *Sorter) Put(key, value []byte) error {
	if s.closed {
		return fmt.Errorf("Sorter: Put after Close")
	}
	if err := s.batch.Put(key, value); err != nil {
		return fmt.Errorf("Sorter: batch.Put: %w", err)
	}
	if s.batch.ValueSize() >= sorterFlushBytes {
		if err := s.batch.Write(); err != nil {
			return fmt.Errorf("Sorter: batch.Write: %w", err)
		}
		s.batch.Reset()
	}
	return nil
}

// Iterate flushes any pending batch, opens a Pebble iterator over the full
// keyspace, and calls yield(k, v) for each entry in ascending key order.
//
// IMPORTANT: yield's key/value slices alias Pebble's internal buffers and
// are invalidated by the next Next() call. Callers that retain either slice
// MUST copy it. The streaming HashBuilder (internal/reth/hash_builder.go)
// already copies AddLeaf inputs at lines 179-182, so feeding it directly is
// safe; document the assumption at any future call site that doesn't copy.
//
// If yield returns a non-nil error, iteration stops immediately and that
// error is returned. Iterator's own Error() takes precedence on internal
// failure.
func (s *Sorter) Iterate(yield func(key, value []byte) error) error {
	if s.closed {
		return fmt.Errorf("Sorter: Iterate after Close")
	}
	if err := s.batch.Write(); err != nil {
		return fmt.Errorf("Sorter: final batch.Write: %w", err)
	}
	s.batch.Reset()

	iter := s.db.NewIterator(nil, nil)
	defer iter.Release()
	for iter.Next() {
		if err := yield(iter.Key(), iter.Value()); err != nil {
			return err
		}
	}
	return iter.Error()
}

// Close flushes the pending batch (best-effort), closes the Pebble DB, and
// removes the on-disk temp directory. Idempotent: subsequent calls return
// nil. RemoveAll errors are logged but not returned — temp-dir cleanup is a
// best-effort hygiene step and should not mask real generation errors.
func (s *Sorter) Close() error {
	if s.closed {
		return nil
	}
	s.closed = true

	// Best-effort flush of any pending writes; if Write fails we still
	// proceed to Close so resources are released.
	if err := s.batch.Write(); err != nil {
		// Continue closing; surface the error after cleanup.
		_ = s.db.Close()
		_ = os.RemoveAll(s.dir)
		return fmt.Errorf("Sorter: final batch.Write: %w", err)
	}
	s.batch.Reset()

	if err := s.db.Close(); err != nil {
		_ = os.RemoveAll(s.dir)
		return fmt.Errorf("Sorter: db.Close: %w", err)
	}
	if err := os.RemoveAll(s.dir); err != nil {
		// Don't propagate — leftover temp dir is hygiene, not correctness.
		log.Printf("Sorter: cleanup %s: %v", s.dir, err)
	}
	return nil
}
