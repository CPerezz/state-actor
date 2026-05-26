package snap

import (
	"context"
	"encoding/binary"
	"errors"
	"fmt"
	"os"

	"github.com/spaolacci/murmur3"

	"github.com/nerolation/state-actor/internal/erigon"
	"github.com/nerolation/state-actor/internal/erigon/btindex"
	"github.com/nerolation/state-actor/internal/erigon/existence"
	"github.com/nerolation/state-actor/internal/erigon/seg"
)

// Writer composes seg + btindex + existence into Erigon's per-domain
// snapshot file set. Construct via NewWriter; emit a domain via
// WriteDomain; finalize via Close.
//
// Re-creating a Writer over an existing datadir is supported: NewWriter
// verifies salt/erigondb invariants match what's already on disk and
// returns an error on mismatch (so we never silently emit a file set
// against a different salt than the existence filters were built with).
type Writer struct {
	datadir   string
	settings  Settings
	closed    bool
}

// NewWriter validates the on-disk salt + erigondb.toml against
// Settings, creates them if absent, ensures the snapshot directory
// layout exists, and returns a Writer ready to emit domains.
//
// Defaults applied to s if zero:
//   - StepSize:          erigon.StepSize (390_625)
//   - StepsInFrozenFile: erigon.StepsInFrozenFile (256)
//   - SnapshotVersion:   erigon.SnapshotFormatVersion ("v1.0")
//   - Salt:              DeriveSaltFromSeed(s.Seed) if both Salt==0
//                        and no salt-state.txt exists yet on disk
//   - Accessors[d]:      DefaultAccessorMask(d) per Verifier B's
//                        correction (per-domain mix)
func NewWriter(datadir string, s Settings) (*Writer, error) {
	if datadir == "" {
		return nil, errors.New("snap.NewWriter: datadir is required")
	}
	if s.StepSize == 0 {
		s.StepSize = erigon.StepSize
	}
	if s.StepsInFrozenFile == 0 {
		s.StepsInFrozenFile = erigon.StepsInFrozenFile
	}
	if s.SnapshotVersion == "" {
		s.SnapshotVersion = erigon.SnapshotFormatVersion
	}
	if s.Accessors == nil {
		s.Accessors = make(map[Domain]AccessorMask, 4)
	}
	for _, d := range []Domain{DomainAccounts, DomainStorage, DomainCode, DomainCommitment} {
		if _, ok := s.Accessors[d]; !ok {
			s.Accessors[d] = DefaultAccessorMask(d)
		}
	}

	if err := EnsureSnapshotLayout(datadir); err != nil {
		return nil, err
	}

	// Salt: if on-disk salt-state.txt exists, it wins (idempotency).
	// Otherwise derive from seed (or use the caller-provided salt) and
	// persist.
	if existingSalt, err := ReadSalt(datadir); err == nil {
		if s.Salt != 0 && s.Salt != existingSalt {
			return nil, fmt.Errorf("snap.NewWriter: salt mismatch: settings=%d, on-disk=%d",
				s.Salt, existingSalt)
		}
		s.Salt = existingSalt
	} else if errors.Is(err, os.ErrNotExist) {
		if s.Salt == 0 {
			s.Salt = DeriveSaltFromSeed(s.Seed)
		}
		if err := WriteSalt(datadir, s.Salt); err != nil {
			return nil, err
		}
	} else {
		return nil, fmt.Errorf("snap.NewWriter: read salt-state.txt: %w", err)
	}

	// erigondb.toml: same idempotency contract — error on mismatch,
	// write if absent.
	if err := ensureErigonDBSettings(datadir, s.StepSize, s.StepsInFrozenFile); err != nil {
		return nil, err
	}

	return &Writer{datadir: datadir, settings: s}, nil
}

func ensureErigonDBSettings(datadir string, want, wantFrozen uint64) error {
	path := ErigonDBSettingsPath(datadir)
	raw, err := os.ReadFile(path)
	if errors.Is(err, os.ErrNotExist) {
		return WriteErigonDBSettings(datadir, want, wantFrozen)
	}
	if err != nil {
		return fmt.Errorf("snap.NewWriter: read erigondb.toml: %w", err)
	}
	// Cheap presence check — Erigon's parser is lenient on whitespace,
	// so we look for the literal "step_size = <want>" and
	// "steps_in_frozen_file = <wantFrozen>" substrings. A real
	// mismatch returns an error rather than silently overwriting.
	wantStep := fmt.Sprintf("step_size = %d", want)
	wantSteps := fmt.Sprintf("steps_in_frozen_file = %d", wantFrozen)
	body := string(raw)
	if !contains(body, wantStep) || !contains(body, wantSteps) {
		return fmt.Errorf("snap.NewWriter: erigondb.toml mismatch: want %q + %q, got %q",
			wantStep, wantSteps, body)
	}
	return nil
}

func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}

// Salt returns the snapshot salt that's persisted at
// <datadir>/snapshots/salt-state.txt.
func (w *Writer) Salt() uint32 { return w.settings.Salt }

// WriteDomain emits the per-domain file set (.kv data + accessors) for
// (d, r). entries MUST yield ascending keys; behaviour is undefined
// otherwise. The data file is written first via seg.Compressor, then
// re-iterated via seg.Decompressor to feed the accessor builders —
// matching the two-pass pattern from Erigon's
// simple_accessor_builder.go:194-216 (Verifier B's Correction 2).
//
// keyCount is required up-front: the bloom-filter sizing
// (existence.NewFilterBuilder) and B+tree node count depend on it.
// If the caller doesn't know the count statically, materialize into a
// slice first.
//
// Output paths (under <datadir>/snapshots/domain/):
//
//	v1.0-accounts.0-256.kv
//	v1.0-accounts.0-256.bt    (AccessorBTree)
//	v1.0-accounts.0-256.kvi   (AccessorHashMap)   — commitment only
//	v1.0-accounts.0-256.kvei  (AccessorExistence) — all domains by default
func (w *Writer) WriteDomain(ctx context.Context, d Domain, r StepRange, keyCount uint64, entries func(yield func(DomainEntry) bool)) error {
	if w.closed {
		return errors.New("snap.Writer: Closed")
	}
	if r.From >= r.To {
		return fmt.Errorf("snap.WriteDomain: invalid StepRange [%d, %d)", r.From, r.To)
	}

	domainDir := DomainDir(w.datadir)
	tmpDir := domainDir // seg.Compressor writes its own .tmp files under tmpDir
	dataPath := BuildDataFilename(domainDir, w.settings.SnapshotVersion, d, r)

	// Pass 1: stream (k, v) into seg.Compressor.
	cfg := seg.DefaultConfig()
	comp, err := seg.NewCompressor(dataPath, tmpDir, cfg)
	if err != nil {
		return fmt.Errorf("snap.WriteDomain: seg.NewCompressor: %w", err)
	}
	// `entries` is a push-style iterator — invoke it with our consumer.
	var addErr error
	entries(func(e DomainEntry) bool {
		if err := comp.AddWord(e.Key); err != nil {
			addErr = fmt.Errorf("AddWord(key): %w", err)
			return false
		}
		if err := comp.AddWord(e.Value); err != nil {
			addErr = fmt.Errorf("AddWord(value): %w", err)
			return false
		}
		return true
	})
	if addErr != nil {
		_ = comp.Close()
		return fmt.Errorf("snap.WriteDomain: pass-1: %w", addErr)
	}
	if err := comp.Compress(); err != nil {
		_ = comp.Close()
		return fmt.Errorf("snap.WriteDomain: seg.Compress: %w", err)
	}
	if err := comp.Close(); err != nil {
		return fmt.Errorf("snap.WriteDomain: seg.Close: %w", err)
	}

	// Pass 2: re-open the .kv, iterate (key, val, keyOff, valOff), feed
	// the accessor builders.
	mask := w.settings.Accessors[d]
	dec, err := seg.NewDecompressor(dataPath)
	if err != nil {
		return fmt.Errorf("snap.WriteDomain: seg.NewDecompressor: %w", err)
	}
	defer dec.Close()

	var bt *btindex.Writer
	if mask.Has(AccessorBTree) {
		btPath := BuildBTreeFilename(domainDir, w.settings.SnapshotVersion, d, r)
		bt, err = btindex.New(btindex.Args{
			KeyCount:  int(keyCount),
			TmpDir:    tmpDir,
			IndexFile: btPath,
		})
		if err != nil {
			return fmt.Errorf("snap.WriteDomain: btindex.New: %w", err)
		}
	}

	var exist *existence.FilterBuilder
	if mask.Has(AccessorExistence) {
		exPath := BuildExistenceFilename(domainDir, w.settings.SnapshotVersion, d, r)
		exist, err = existence.NewFilterBuilder(keyCount, exPath, false)
		if err != nil {
			return fmt.Errorf("snap.WriteDomain: existence.NewFilterBuilder: %w", err)
		}
	}

	// AccessorHashMap (recsplit) is commitment-domain only. Wiring left
	// as a TODO until internal/erigon/recsplit/ spike lands.
	if mask.Has(AccessorHashMap) {
		// TODO(plan Task 72 / recsplit): once internal/erigon/recsplit
		// exposes a Writer matching the plan's API contract, instantiate
		// it here and call AddKey(key, keyOffset) on every iterated
		// entry. Skipping for now keeps the value domains (Accounts,
		// Storage, Code) fully functional.
		_ = mask
	}

	// Salt-prehash for the existence filter (per Verifier B's note in
	// the plan: caller is responsible for the salt pre-hash):
	//   hash := murmur3.Sum128WithSeed(key, salt)
	// We use the first uint64 of the 128-bit hash because the existence
	// filter's AddHash signature accepts a uint64. Erigon's writer uses
	// the same lower 64 bits.
	saltBytes := make([]byte, 4)
	binary.BigEndian.PutUint32(saltBytes, w.settings.Salt)

	for entry, err := range dec.Iterate(ctx) {
		if err != nil {
			return fmt.Errorf("snap.WriteDomain: decompressor iterate: %w", err)
		}
		if bt != nil {
			if err := bt.AddKey(entry.Key, entry.ValueOffset); err != nil {
				return fmt.Errorf("snap.WriteDomain: btindex.AddKey: %w", err)
			}
		}
		if exist != nil {
			lo, _ := murmur3.Sum128WithSeed(entry.Key, w.settings.Salt)
			if err := exist.AddHash(lo); err != nil {
				return fmt.Errorf("snap.WriteDomain: existence.AddHash: %w", err)
			}
		}
	}

	if bt != nil {
		if err := bt.Build(ctx); err != nil {
			return fmt.Errorf("snap.WriteDomain: btindex.Build: %w", err)
		}
		if err := bt.Close(); err != nil {
			return fmt.Errorf("snap.WriteDomain: btindex.Close: %w", err)
		}
	}
	if exist != nil {
		if err := exist.Build(); err != nil {
			return fmt.Errorf("snap.WriteDomain: existence.Build: %w", err)
		}
	}
	return nil
}

// Close marks the Writer as no-longer-usable. Snapshot files are
// already fsynced at WriteDomain return; Close is reserved for future
// metadata-flush hooks.
func (w *Writer) Close() error {
	w.closed = true
	return nil
}
