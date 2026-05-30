package existence

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"

	bloomfilter "github.com/holiman/bloomfilter/v2"
)

// FalsePositiveRate is the bloom-filter target false-positive probability,
// pinned to match Erigon's `bloomfilter.OptimalM(keysCount, 0.01)` call
// at `db/datastruct/existence/existence_filter.go:60`.
const FalsePositiveRate = 0.01

// ErrFuseFilterUnsupported is returned by NewFilterBuilder when
// useFuse=true. Erigon's production caller path always passes false
// (`db/datastruct/btindex/btree_index.go:397`); state-actor v1 ships
// bloom-only and defers the FuseFilter wire format to v2.
var ErrFuseFilterUnsupported = errors.New("existence: useFuse=true is reserved for v2 (bloom-only in v1)")

// FilterBuilder writes a single Erigon-compatible `.kvei` file. Single
// use: AddHash N times then Build. Concurrent calls are unsafe.
//
// Mirrors `existence.Filter` (existence_filter.go:34-42), trimmed to
// the writer-only subset. The reader-side fields (`fuseReader`,
// `useFuse=true` paths) are intentionally omitted — state-actor never
// reads `.kvei` back, Erigon does that at startup.
type FilterBuilder struct {
	filter   *bloomfilter.Filter // nil iff empty (keysCount < 2)
	empty    bool
	filePath string
	fileName string
	noFsync  bool // disables fsync for tests
}

// NewFilterBuilder constructs a builder targeting filePath. On Build
// the bytes are written via temp file + rename for crash safety,
// mirroring Erigon (existence_filter.go:113-128).
//
// `keysCount` is the expected number of AddHash calls; it sizes the
// bit array via `bloomfilter.OptimalM(keysCount, 0.01)`. Per Erigon's
// convention (existence_filter.go:48-50), when keysCount < 2 the
// filter is marked empty and Build emits a zero-length file.
//
// `useFuse=true` returns ErrFuseFilterUnsupported in v1.
func NewFilterBuilder(keysCount uint64, filePath string, useFuse bool) (*FilterBuilder, error) {
	if useFuse {
		return nil, ErrFuseFilterUnsupported
	}
	_, fileName := filepath.Split(filePath)
	b := &FilterBuilder{filePath: filePath, fileName: fileName}
	if keysCount < 2 {
		b.empty = true
		return b, nil
	}
	m := bloomfilter.OptimalM(keysCount, FalsePositiveRate)
	f, err := bloomfilter.New(m)
	if err != nil {
		return nil, fmt.Errorf("existence: bloomfilter.New(m=%d): %w (%s)", m, err, fileName)
	}
	b.filter = f
	return b, nil
}

// newFilterBuilderWithKeys is an internal helper for byte-equality
// tests: it bypasses the CSPRNG key generator in NewFilterBuilder and
// produces a filter with caller-supplied keys. Production code must
// never call this — the keys MUST be random to give the bloom filter
// its probabilistic guarantee. The function is unexported on purpose;
// only the same-package test file can reach it.
//
// `keys` must have length `bloomfilter.HardCodedK` (=3) and contain
// distinct values; otherwise `bloomfilter.NewWithKeys` returns an error.
func newFilterBuilderWithKeys(keysCount uint64, filePath string, keys [bloomfilter.HardCodedK]uint64) (*FilterBuilder, error) {
	_, fileName := filepath.Split(filePath)
	b := &FilterBuilder{filePath: filePath, fileName: fileName}
	if keysCount < 2 {
		b.empty = true
		return b, nil
	}
	m := bloomfilter.OptimalM(keysCount, FalsePositiveRate)
	f, err := bloomfilter.NewWithKeys(m, keys[:])
	if err != nil {
		return nil, fmt.Errorf("existence: bloomfilter.NewWithKeys(m=%d): %w (%s)", m, err, fileName)
	}
	b.filter = f
	return b, nil
}

// AddHash inserts an already-hashed key into the filter. The caller is
// responsible for the salt pre-hash (`murmur3.Sum128WithSeed(key, salt)`);
// this method mirrors Erigon's `Filter.AddHash` (existence_filter.go:69-81)
// which forwards directly to `bloomfilter.Filter.AddHash`.
//
// Returns nil for empty builders (matches Erigon's no-op behavior at
// existence_filter.go:70-72). Returns an error after Build (the underlying
// filter is consumed; further inserts would not be persisted).
func (b *FilterBuilder) AddHash(hash uint64) error {
	if b.empty {
		return nil
	}
	if b.filter == nil {
		return errors.New("existence: AddHash after Build / on closed builder")
	}
	b.filter.AddHash(hash)
	return nil
}

// Build serializes the filter to disk at filePath. Uses Erigon's exact
// pattern (existence_filter.go:98-131):
//
//  1. Empty filter → create zero-length file at filePath, close.
//  2. Non-empty → write to <filePath>.<rand>.tmp, fsync, rename to filePath.
//
// Build is single-shot: after success the builder's internal filter is
// released (set to nil) and further AddHash returns an error.
func (b *FilterBuilder) Build() error {
	if b.empty {
		// Explicit 0o644 — see seg/rawwords.go for the umask-induced
		// 0o600 issue we hit on the bench host when the daemon
		// (separate container, different uid) couldn't read snapshot
		// files state-actor wrote.
		cf, err := os.OpenFile(b.filePath, os.O_RDWR|os.O_CREATE|os.O_TRUNC, 0o644)
		if err != nil {
			return fmt.Errorf("existence: create empty %s: %w", b.fileName, err)
		}
		return cf.Close()
	}
	if b.filter == nil {
		return errors.New("existence: Build called twice")
	}

	cf, err := createTemp(b.filePath)
	if err != nil {
		return fmt.Errorf("existence: createTemp %s: %w", b.fileName, err)
	}
	tmpPath := cf.Name()
	closed := false
	defer func() {
		if !closed {
			_ = cf.Close()
		}
		// If we never renamed, clean up the stray temp.
		if _, statErr := os.Stat(tmpPath); statErr == nil {
			_ = os.Remove(tmpPath)
		}
	}()

	if _, err := b.filter.WriteTo(cf); err != nil {
		return fmt.Errorf("existence: WriteTo %s: %w", b.fileName, err)
	}
	if err := b.fsync(cf); err != nil {
		return err
	}
	if err := cf.Close(); err != nil {
		return fmt.Errorf("existence: close %s: %w", b.fileName, err)
	}
	closed = true
	if err := os.Rename(tmpPath, b.filePath); err != nil {
		return fmt.Errorf("existence: rename %s -> %s: %w", tmpPath, b.filePath, err)
	}

	// Release the filter so AddHash after Build fails loudly.
	b.filter = nil
	return nil
}

// DisableFsync turns off the fsync inside Build for this builder. Use
// only in tests where durability is irrelevant; mirrors Erigon's
// `Filter.DisableFsync` (existence_filter.go:134-142).
func (b *FilterBuilder) DisableFsync() {
	b.noFsync = true
}

// fsync mirrors `(b *Filter) fsync` (existence_filter.go:147-156).
func (b *FilterBuilder) fsync(f *os.File) error {
	if b.noFsync {
		return nil
	}
	if err := f.Sync(); err != nil {
		return fmt.Errorf("existence: fsync %s: %w", b.fileName, err)
	}
	return nil
}

// createTemp mirrors `dir.CreateTemp` (common/dir/rw_dir.go:231-243).
// We reimplement here to avoid pulling Erigon's `common/dir` package
// into the main module — pattern is "<basename>.*.tmp" so Erigon's
// startup cleanup of `.tmp` files at common/dir/rw_dir.go would also
// pick up state-actor's stragglers if a write was interrupted.
func createTemp(file string) (*os.File, error) {
	dir, base := filepath.Split(file)
	if dir == "" {
		dir = "."
	}
	pattern := fmt.Sprintf("%s.*.tmp", base)
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return nil, err
	}
	// os.CreateTemp hardcodes 0o600 — chmod to 0o644 so the final
	// renamed .kvei is world-readable by the downstream daemon.
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return nil, fmt.Errorf("existence: chmod temp: %w", err)
	}
	return f, nil
}
