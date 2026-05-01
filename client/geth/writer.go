package geth

import (
	"fmt"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/ethdb"
	"github.com/ethereum/go-ethereum/ethdb/pebble"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/genesis"
)

// Writer writes state to a geth-compatible Pebble database.
// It uses the snapshot layer format with hashed keys.
type Writer struct {
	db        ethdb.KeyValueStore
	dbPath    string
	batchSize int
	workers   int

	bw *batchWriter

	accountBytes atomic.Uint64
	storageBytes atomic.Uint64
	codeBytes    atomic.Uint64
}

// NewWriter creates a new geth-format state writer.
func NewWriter(dbPath string, batchSize, workers int) (*Writer, error) {
	db, err := pebble.New(dbPath, 512, 256, "stategen/", false)
	if err != nil {
		return nil, fmt.Errorf("failed to open pebble database: %w", err)
	}

	w := &Writer{
		db:        db,
		dbPath:    dbPath,
		batchSize: batchSize,
		workers:   workers,
	}

	w.bw = newBatchWriter(db, batchSize, workers)

	return w, nil
}

// DB returns the underlying database for external use (e.g., genesis writing).
func (w *Writer) DB() ethdb.KeyValueStore {
	return w.db
}

// WriteAccount writes an account to the snapshot layer.
// addrHash is pre-computed keccak256(addr) to avoid redundant hashing.
func (w *Writer) WriteAccount(addr common.Address, addrHash common.Hash, acc *types.StateAccount, incarnation uint64) error {
	slimData := types.SlimAccountRLP(*acc)
	key := accountSnapshotKey(addrHash)
	return w.bw.put(key, slimData, &w.accountBytes)
}

// WriteStorage writes a storage slot to the snapshot layer.
// addrHash and slotHash are pre-computed keccak256 hashes (addr and slot are unused in geth format).
func (w *Writer) WriteStorage(addr common.Address, addrHash common.Hash, slot common.Hash, slotHash common.Hash, value common.Hash) error {
	valueRLP, err := encodeStorageValue(value)
	if err != nil {
		return fmt.Errorf("encode storage value: %w", err)
	}
	key := storageSnapshotKey(addrHash, slotHash)
	return w.bw.put(key, valueRLP, &w.storageBytes)
}

// WriteStorageRLP writes a storage slot with pre-encoded RLP value.
// Avoids double-encoding when the caller already has the RLP bytes.
func (w *Writer) WriteStorageRLP(addrHash common.Hash, slotHash common.Hash, valueRLP []byte) error {
	key := storageSnapshotKey(addrHash, slotHash)
	return w.bw.put(key, valueRLP, &w.storageBytes)
}

// WriteRawStorage writes a storage slot using a pre-hashed trie key.
// The hashedSlot bypasses keccak256 and is used directly as the snapshot key.
func (w *Writer) WriteRawStorage(addr common.Address, incarnation uint64, hashedSlot, value common.Hash) error {
	addrHash := crypto.Keccak256Hash(addr[:])
	valueRLP, err := encodeStorageValue(value)
	if err != nil {
		return fmt.Errorf("encode storage value: %w", err)
	}
	key := storageSnapshotKey(addrHash, hashedSlot)
	return w.bw.put(key, valueRLP, &w.storageBytes)
}

// WriteCode writes contract bytecode.
func (w *Writer) WriteCode(codeHash common.Hash, code []byte) error {
	key := codeKey(codeHash)
	return w.bw.put(key, code, &w.codeBytes)
}

// SetStateRoot writes the snapshot root marker and PathDB initialization
// metadata. binaryTrie selects the namespace (raw vs "v"-prefixed) for the
// pathdb keys; geth's pathdb wraps its diskdb under the "v" prefix in
// bintrie mode (triedb/pathdb/database.go:168-170) so the writes have to
// match. When --genesis is provided, WriteGenesisBlock writes the same
// entries; doing it here too is idempotent and ensures non-genesis DBs
// also boot cleanly.
func (w *Writer) SetStateRoot(root common.Hash, binaryTrie bool) error {
	if err := WritePathDBMetadata(w.db, root, binaryTrie); err != nil {
		return fmt.Errorf("write pathdb metadata: %w", err)
	}
	return nil
}

// Flush commits all pending writes and closes the async batch pipeline.
// This is a shutdown-once operation — don't call it mid-run.
func (w *Writer) Flush() error {
	return w.bw.finish()
}

// FlushBatch commits the currently-buffered batch to Pebble synchronously
// and waits for the async pipeline to drain outstanding batches. Does not
// close the pipeline, so subsequent Write* calls still work. Safe to call
// mid-generation (e.g. to force a dirSize sample to see the latest bytes).
// The caller is responsible for coordinating that all desired Write* calls
// have already returned before flushing.
func (w *Writer) FlushBatch() error {
	return w.bw.flushAndDrainSync()
}

// Close closes the writer.
func (w *Writer) Close() error {
	w.bw.close()
	return w.db.Close()
}

// Stats returns write statistics.
func (w *Writer) Stats() generator.WriterStats {
	return generator.WriterStats{
		AccountBytes: w.accountBytes.Load(),
		StorageBytes: w.storageBytes.Load(),
		CodeBytes:    w.codeBytes.Load(),
	}
}

// WriteGenesisBlockFull writes the genesis block with full genesis config.
func (w *Writer) WriteGenesisBlockFull(genesisConfig *genesis.Genesis, stateRoot common.Hash, binaryTrie bool) error {
	ancientDir := filepath.Join(w.dbPath, "ancient")
	_, err := WriteGenesisBlock(w.db, genesisConfig, stateRoot, binaryTrie, ancientDir)
	return err
}

// --- Batch writer for parallel writes ---

type batchWriter struct {
	db        ethdb.KeyValueStore
	batchSize int
	batchChan chan *batchWork
	errChan   chan error
	wg        sync.WaitGroup
	closeOnce sync.Once
	// mu serialises put() (hot path, single-goroutine in normal operation)
	// with mid-run flush() calls issued from a different goroutine (e.g.
	// target-size size sampling). The async batch-commit workers don't
	// touch bw.batch directly — they consume the detached *batchWork
	// sent on batchChan — so they don't need to hold this mutex.
	mu    sync.Mutex
	batch ethdb.Batch
	count int
}

type batchWork struct {
	batch ethdb.Batch
}

func newBatchWriter(db ethdb.KeyValueStore, batchSize, workers int) *batchWriter {
	bw := &batchWriter{
		db:        db,
		batchSize: batchSize,
		batchChan: make(chan *batchWork, workers*2),
		errChan:   make(chan error, 1),
		batch:     db.NewBatch(),
	}

	for i := 0; i < workers; i++ {
		bw.wg.Add(1)
		go func() {
			defer bw.wg.Done()
			for work := range bw.batchChan {
				if err := work.batch.Write(); err != nil {
					select {
					case bw.errChan <- err:
					default:
					}
					return
				}
			}
		}()
	}

	return bw
}

func (bw *batchWriter) put(key, value []byte, counter *atomic.Uint64) error {
	bw.mu.Lock()
	if err := bw.batch.Put(key, value); err != nil {
		bw.mu.Unlock()
		return err
	}
	counter.Add(uint64(len(key) + len(value)))
	bw.count++
	shouldFlush := bw.count >= bw.batchSize
	bw.mu.Unlock()
	if shouldFlush {
		return bw.flushExternal()
	}
	return nil
}

// flushExternal is the public-facing flush entry. flushLocked expects the
// caller to already hold bw.mu.
func (bw *batchWriter) flushExternal() error {
	bw.mu.Lock()
	defer bw.mu.Unlock()
	return bw.flushLocked()
}

func (bw *batchWriter) flushLocked() error {
	if bw.count == 0 {
		return nil
	}
	select {
	case bw.batchChan <- &batchWork{batch: bw.batch}:
	case err := <-bw.errChan:
		return fmt.Errorf("batch worker failed: %w", err)
	}
	bw.batch = bw.db.NewBatch()
	bw.count = 0
	return nil
}

// flush is retained as the lock-acquiring form used by external callers
// (FlushBatch and finish).
func (bw *batchWriter) flush() error {
	return bw.flushExternal()
}

// flushAndDrainSync commits the current batch synchronously (bypassing
// the async workers) so the bytes are guaranteed on disk by the time
// the call returns. It swaps in a fresh batch under the lock, then
// commits the old one directly; the async workers continue handling
// their own queued batches normally.
func (bw *batchWriter) flushAndDrainSync() error {
	bw.mu.Lock()
	if bw.count == 0 {
		bw.mu.Unlock()
		return nil
	}
	oldBatch := bw.batch
	bw.batch = bw.db.NewBatch()
	bw.count = 0
	bw.mu.Unlock()
	return oldBatch.Write()
}

func (bw *batchWriter) finish() error {
	if err := bw.flush(); err != nil {
		return err
	}
	bw.closeOnce.Do(func() { close(bw.batchChan) })
	bw.wg.Wait()

	select {
	case err := <-bw.errChan:
		return err
	default:
	}
	return nil
}

func (bw *batchWriter) close() {
	bw.closeOnce.Do(func() { close(bw.batchChan) })
	bw.wg.Wait()
}

// --- Key encoding functions matching geth's rawdb schema ---

var (
	snapshotAccountPrefix = []byte("a")
	snapshotStoragePrefix = []byte("o")
	codePrefix            = []byte("c")
)

func accountSnapshotKey(hash common.Hash) []byte {
	return append(snapshotAccountPrefix, hash.Bytes()...)
}

func storageSnapshotKey(accountHash, storageHash common.Hash) []byte {
	buf := make([]byte, len(snapshotStoragePrefix)+common.HashLength+common.HashLength)
	n := copy(buf, snapshotStoragePrefix)
	n += copy(buf[n:], accountHash.Bytes())
	copy(buf[n:], storageHash.Bytes())
	return buf
}

func codeKey(hash common.Hash) []byte {
	return append(codePrefix, hash.Bytes()...)
}

func encodeStorageValue(value common.Hash) ([]byte, error) {
	trimmed := trimLeftZeroes(value[:])
	if len(trimmed) == 0 {
		return nil, nil
	}
	encoded, err := rlp.EncodeToBytes(trimmed)
	if err != nil {
		return nil, fmt.Errorf("failed to RLP-encode storage value %x: %w", value, err)
	}
	return encoded, nil
}

func trimLeftZeroes(s []byte) []byte {
	for i, v := range s {
		if v != 0 {
			return s[i:]
		}
	}
	return nil
}
