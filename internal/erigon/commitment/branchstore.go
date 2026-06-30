//go:build cgo_erigon_commitment

package commitment

import (
	"fmt"
	"os"

	"github.com/cockroachdb/pebble"
)

// branchStore is a LIVE, concurrency-safe read-write KV over a temp Pebble
// DB — the commitment branch sink. It replaces the in-memory
// mergedBranches map, which was Θ(total-keys) (tens of GB at bench scale
// because storage slots are hashed into the same unified trie). PutBranch →
// set, Branch → get, both live and safe for the 16 ConcurrentPatriciaHashed
// workers (Pebble Set/Get are goroutine-safe). Peak RAM is the Pebble
// memtable + cache, independent of branch count.
//
// Being read-write (not the write-once streamsort) is what enables
// incremental/chunked commitment: branches written by an earlier chunk's
// Process are visible to a later chunk's ctx.Branch reads. For a single
// from-empty genesis Process the read path is never hit for a written
// prefix (sorted single-pass folds each prefix once and never re-descends
// it), so get() returns nil there — byte-identical to the old map path.
type branchStore struct {
	dir string
	db  *pebble.DB
}

// newBranchStore opens a fresh temp Pebble DB under tmpDir (empty →
// os.TempDir()).
func newBranchStore(tmpDir string) (*branchStore, error) {
	dir, err := os.MkdirTemp(tmpDir, "commitment-branches-*")
	if err != nil {
		return nil, fmt.Errorf("commitment: mkdir branch store: %w", err)
	}
	db, err := pebble.Open(dir, &pebble.Options{DisableWAL: true})
	if err != nil {
		_ = os.RemoveAll(dir)
		return nil, fmt.Errorf("commitment: open branch store: %w", err)
	}
	return &branchStore{dir: dir, db: db}, nil
}

// set writes one branch (last-write-wins on a re-fold). Safe for the 16
// nibble-disjoint workers to call concurrently. The distinct-prefix count
// (== old len(mergedBranches)) is taken once at the end via iterate, so
// there's no per-write read amplification here.
func (b *branchStore) set(prefix, data []byte) error {
	if err := b.db.Set(prefix, data, pebble.NoSync); err != nil {
		return fmt.Errorf("commitment: branch store set: %w", err)
	}
	return nil
}

// get returns the branch at prefix, or (nil, nil) if absent. The returned
// slice is copied out of Pebble's buffer.
func (b *branchStore) get(prefix []byte) ([]byte, error) {
	v, closer, err := b.db.Get(prefix)
	if err == pebble.ErrNotFound {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("commitment: branch store get: %w", err)
	}
	out := make([]byte, len(v))
	copy(out, v)
	_ = closer.Close()
	return out, nil
}

// iterate yields every (prefix, data) in ascending key order. Called once
// after the walk to dump branches into the write-once commitment .kv
// streamsort. Key/value alias Pebble's buffers — copy if retained.
func (b *branchStore) iterate(yield func(prefix, data []byte) error) error {
	it, err := b.db.NewIter(nil)
	if err != nil {
		return fmt.Errorf("commitment: branch store iter: %w", err)
	}
	defer func() { _ = it.Close() }()
	for it.First(); it.Valid(); it.Next() {
		if err := yield(it.Key(), it.Value()); err != nil {
			return err
		}
	}
	return it.Error()
}

// close releases the DB and removes the temp dir. Idempotent.
func (b *branchStore) close() {
	if b.db != nil {
		_ = b.db.Close()
		b.db = nil
	}
	if b.dir != "" {
		_ = os.RemoveAll(b.dir)
		b.dir = ""
	}
}
