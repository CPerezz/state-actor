//go:build cgo_neth

package nethermind

import (
	"fmt"
	"sync"

	"github.com/linxGnu/grocksdb"

	nethstorage "github.com/nerolation/state-actor/internal/neth/storage"
)

// stateDBSink writes state-trie nodes to the State DB using HalfPath keys.
// This is the bridge between internal/neth/trie.Builder (which emits
// OnTrieNode callbacks) and the State RocksDB Nethermind reads on boot.
//
// Writes are buffered into a grocksdb WriteBatch and flushed when the
// pending size hits stateBatchFlushBytes — synchronous Put-per-node went
// fsync-bound at 5M+500K scale. The batch is flushed (and the sink can
// be safely closed) by calling flush() before reading the State DB.
type stateDBSink struct {
	db *grocksdb.DB
	wo *grocksdb.WriteOptions
	wb *grocksdb.WriteBatch

	// pendingBytes tracks the live WriteBatch's payload size; we flush
	// when it crosses stateBatchFlushBytes to keep memory bounded for
	// 50GB-scale runs that emit hundreds of millions of trie nodes.
	pendingBytes int
}

// stateBatchFlushBytes is the WriteBatch flush threshold. 64 MiB matches
// geth's defaultFlushBytes and besu's flushThresholdBytes — at 16 MiB
// (the pre-tuning value) the v5 bench paid 4x the per-flush RocksDB
// commit overhead, contributing materially to the 2:42 wall time.
const stateBatchFlushBytes = 64 * 1024 * 1024

func newStateDBSink(db *grocksdb.DB) *stateDBSink {
	wo := grocksdb.NewDefaultWriteOptions()
	// Genesis is one-shot; durability comes from the final CompactRange
	// + Close. Skipping WAL on the bulk-flush path saves ~50% of
	// RocksDB's write amplification (matches besu commit 4847945).
	wo.DisableWAL(true)
	return &stateDBSink{
		db: db,
		wo: wo,
		wb: grocksdb.NewWriteBatch(),
	}
}

// flush writes any pending entries and resets the WriteBatch. Safe to
// call repeatedly — a no-op when nothing is buffered.
func (s *stateDBSink) flush() error {
	if s.pendingBytes == 0 {
		return nil
	}
	if err := s.db.Write(s.wo, s.wb); err != nil {
		return fmt.Errorf("stateDBSink flush: %w", err)
	}
	s.wb.Clear()
	s.pendingBytes = 0
	return nil
}

func (s *stateDBSink) close() error {
	err := s.flush()
	if s.wb != nil {
		s.wb.Destroy()
		s.wb = nil
	}
	if s.wo != nil {
		s.wo.Destroy()
		s.wo = nil
	}
	return err
}

func (s *stateDBSink) put(key, value []byte) error {
	s.wb.Put(key, value)
	s.pendingBytes += len(key) + len(value)
	if s.pendingBytes >= stateBatchFlushBytes {
		return s.flush()
	}
	return nil
}

func (s *stateDBSink) SetStateNode(path []byte, pathLen int, keccak [32]byte, rlpBlob []byte) error {
	return s.put(nethstorage.StateNodeKey(path, pathLen, keccak), rlpBlob)
}

// SetStorageNode writes a storage-trie node at its HalfPath storage key
// (74 bytes: section(=2) + addrHash(32) + path[:8] + pathLen + keccak).
func (s *stateDBSink) SetStorageNode(addrHash [32]byte, path []byte, pathLen int, keccak [32]byte, rlpBlob []byte) error {
	return s.put(nethstorage.StorageNodeKey(addrHash, path, pathLen, keccak), rlpBlob)
}

// codeDBSink mirrors stateDBSink for the dbs.code RocksDB. Without this,
// genesis-alloc code + synthetic-contract code were each a separate
// db.Put with WAL-enabled WriteOptions — both an fsync-bound bottleneck
// and a data race once Phase 0 became parallel. The same 64 MiB flush
// threshold + DisableWAL(true) pattern from stateDBSink applies here.
type codeDBSink struct {
	db *grocksdb.DB
	wo *grocksdb.WriteOptions
	wb *grocksdb.WriteBatch
	mu sync.Mutex // codeDBSink may be shared across Phase 0 workers
	// pendingBytes is the live WriteBatch payload size; flush at the
	// stateBatchFlushBytes threshold (same constant — both DBs see
	// similar bulk-write volumes during a 100 GB-target gen).
	pendingBytes int
}

func newCodeDBSink(db *grocksdb.DB) *codeDBSink {
	wo := grocksdb.NewDefaultWriteOptions()
	// Same WAL-skip rationale as stateDBSink (line 38-43).
	wo.DisableWAL(true)
	return &codeDBSink{
		db: db,
		wo: wo,
		wb: grocksdb.NewWriteBatch(),
	}
}

func (s *codeDBSink) put(key, value []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.wb.Put(key, value)
	s.pendingBytes += len(key) + len(value)
	if s.pendingBytes >= stateBatchFlushBytes {
		return s.flushLocked()
	}
	return nil
}

// flushLocked writes any pending entries and resets the WriteBatch.
// Caller must hold s.mu. Safe to call repeatedly — a no-op when nothing
// is buffered.
func (s *codeDBSink) flushLocked() error {
	if s.pendingBytes == 0 {
		return nil
	}
	if err := s.db.Write(s.wo, s.wb); err != nil {
		return fmt.Errorf("codeDBSink flush: %w", err)
	}
	s.wb.Clear()
	s.pendingBytes = 0
	return nil
}

func (s *codeDBSink) close() error {
	s.mu.Lock()
	defer s.mu.Unlock()
	err := s.flushLocked()
	if s.wb != nil {
		s.wb.Destroy()
		s.wb = nil
	}
	if s.wo != nil {
		s.wo.Destroy()
		s.wo = nil
	}
	return err
}
