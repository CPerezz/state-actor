package existence

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"

	bloomfilter "github.com/holiman/bloomfilter/v2"
)

// fixture mirrors the JSON schema produced by
// internal/erigon/_fixtures/cmd/existence/main.go. The BloomKeys field
// captures the three random uint64 keys Erigon's bloom filter chose at
// fixture-generation time; the test reconstructs an identical filter
// via newFilterBuilderWithKeys so byte-equality can be asserted.
//
// BloomKeys is nil for fixtures whose keys_count < 2 — those are the
// "empty .kvei" cases where the file is zero bytes by design.
type fixture struct {
	Label       string   `json:"label"`
	KeysCount   uint64   `json:"keys_count"`
	Inputs      []uint64 `json:"inputs"`
	BloomKeys   []uint64 `json:"bloom_keys"`
	ExpectedHex string   `json:"expected_hex"`
}

// TestGoldenAgainstErigon is the load-bearing byte-equality test: for
// every fixture captured by running Erigon's reference
// existence.Filter (see internal/erigon/_fixtures/cmd/existence/main.go),
// our pure-Go FilterBuilder must produce byte-identical output.
//
// This is the cross-verify check Architect B's "own the format" design
// hinges on — if any byte diverges, our writer will produce
// Erigon-unreadable .kvei files at runtime.
//
// Regenerate fixtures by running:
//
//	cd internal/erigon/_fixtures
//	go run -tags erigon_gen ./cmd/existence \
//	    --out=../../existence/testdata/erigon_golden.json
//
// Bumping internal/erigon/constants.go PinnedErigonCommit triggers a
// regen via `make regen-erigon-fixtures`.
func TestGoldenAgainstErigon(t *testing.T) {
	path := filepath.Join("testdata", "erigon_golden.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (regenerate via _fixtures/cmd/existence)", path, err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures in golden file")
	}

	for _, f := range fixtures {
		f := f
		t.Run(f.Label, func(t *testing.T) {
			want, err := hex.DecodeString(f.ExpectedHex)
			if err != nil {
				t.Fatalf("decode expected hex: %v", err)
			}

			outDir := t.TempDir()
			outPath := filepath.Join(outDir, f.Label+".kvei")

			var b *FilterBuilder
			if f.KeysCount < 2 {
				// Empty path: ordinary NewFilterBuilder; no keys to pin.
				b, err = NewFilterBuilder(f.KeysCount, outPath, false)
			} else {
				if len(f.BloomKeys) != bloomfilter.HardCodedK {
					t.Fatalf("fixture has %d bloom_keys, want %d", len(f.BloomKeys), bloomfilter.HardCodedK)
				}
				var keys [bloomfilter.HardCodedK]uint64
				copy(keys[:], f.BloomKeys)
				b, err = newFilterBuilderWithKeys(f.KeysCount, outPath, keys)
			}
			if err != nil {
				t.Fatalf("NewFilterBuilder(%d, %s, false): %v", f.KeysCount, f.Label, err)
			}
			b.DisableFsync() // tempfs friendliness
			for _, h := range f.Inputs {
				if err := b.AddHash(h); err != nil {
					t.Fatalf("AddHash(%d): %v", h, err)
				}
			}
			if err := b.Build(); err != nil {
				t.Fatalf("Build: %v", err)
			}
			got, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("read %s: %v", outPath, err)
			}
			if !bytes.Equal(got, want) {
				firstDiff := -1
				n := len(got)
				if len(want) < n {
					n = len(want)
				}
				for i := 0; i < n; i++ {
					if got[i] != want[i] {
						firstDiff = i
						break
					}
				}
				t.Fatalf("byte mismatch for %s: first differs at byte %d; lengths got=%d want=%d\n  got (first 64): %x\n  want (first 64): %x",
					f.Label, firstDiff, len(got), len(want),
					prefix(got, 64), prefix(want, 64))
			}
		})
	}
}

// TestFuseRejectedInV1 pins that useFuse=true returns
// ErrFuseFilterUnsupported. Per Verifier B's Correction 3, v1 ships
// bloom-only.
func TestFuseRejectedInV1(t *testing.T) {
	tmp := filepath.Join(t.TempDir(), "x.kvei")
	_, err := NewFilterBuilder(100, tmp, true)
	if !errors.Is(err, ErrFuseFilterUnsupported) {
		t.Fatalf("NewFilterBuilder(_, _, true): got err=%v, want ErrFuseFilterUnsupported", err)
	}
}

// TestEmptyFilterZeroLengthFile pins Erigon's
// `keysCount < 2 → empty file` behavior (existence_filter.go:48-50,
// 99-104).
func TestEmptyFilterZeroLengthFile(t *testing.T) {
	for _, n := range []uint64{0, 1} {
		n := n
		t.Run("", func(t *testing.T) {
			outPath := filepath.Join(t.TempDir(), "empty.kvei")
			b, err := NewFilterBuilder(n, outPath, false)
			if err != nil {
				t.Fatalf("NewFilterBuilder(%d): %v", n, err)
			}
			b.DisableFsync()
			if err := b.AddHash(42); err != nil {
				t.Fatalf("AddHash on empty builder: %v", err)
			}
			if err := b.Build(); err != nil {
				t.Fatalf("Build: %v", err)
			}
			info, err := os.Stat(outPath)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if info.Size() != 0 {
				t.Fatalf("empty .kvei size: got=%d, want=0", info.Size())
			}
		})
	}
}

// TestBuildTwiceRejected pins single-use: a second Build call after
// success must fail loudly rather than silently re-emit.
func TestBuildTwiceRejected(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "x.kvei")
	b, err := NewFilterBuilder(10, outPath, false)
	if err != nil {
		t.Fatalf("NewFilterBuilder: %v", err)
	}
	b.DisableFsync()
	for i := 0; i < 5; i++ {
		if err := b.AddHash(uint64(i)); err != nil {
			t.Fatalf("AddHash(%d): %v", i, err)
		}
	}
	if err := b.Build(); err != nil {
		t.Fatalf("first Build: %v", err)
	}
	if err := b.Build(); err == nil {
		t.Fatal("second Build returned nil; want error")
	}
}

// TestAddHashAfterBuildRejected pins that AddHash on a consumed builder
// returns an error.
func TestAddHashAfterBuildRejected(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "x.kvei")
	b, err := NewFilterBuilder(10, outPath, false)
	if err != nil {
		t.Fatalf("NewFilterBuilder: %v", err)
	}
	b.DisableFsync()
	if err := b.AddHash(1); err != nil {
		t.Fatalf("AddHash: %v", err)
	}
	if err := b.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := b.AddHash(2); err == nil {
		t.Fatal("AddHash after Build returned nil; want error")
	}
}

// TestWireFormatHeader pins the 12-byte magic on a non-empty filter.
// AskAlexSharov/bloomfilter v2.0.9 emits "\x00\x00\x00\x00\x00\x00\x00\x00v02\n"
// at offset 0 (binarymarshaler.go:30).
func TestWireFormatHeader(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "x.kvei")
	b, err := NewFilterBuilder(10, outPath, false)
	if err != nil {
		t.Fatalf("NewFilterBuilder: %v", err)
	}
	b.DisableFsync()
	for i := 0; i < 5; i++ {
		if err := b.AddHash(uint64(i)); err != nil {
			t.Fatalf("AddHash: %v", err)
		}
	}
	if err := b.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	raw, err := os.ReadFile(outPath)
	if err != nil {
		t.Fatalf("read: %v", err)
	}
	if len(raw) < 12 {
		t.Fatalf("file shorter than magic header: %d bytes", len(raw))
	}
	wantMagic := []byte{0, 0, 0, 0, 0, 0, 0, 0, 'v', '0', '2', '\n'}
	if !bytes.Equal(raw[:12], wantMagic) {
		t.Fatalf("magic mismatch: got=%x want=%x", raw[:12], wantMagic)
	}
}

// TestRoundTripParseable verifies that bytes produced by our builder
// can be read back via the same bloomfilter library's reader. If this
// passes but TestGoldenAgainstErigon fails, the bug is in our writer's
// random-key choice or fsync placement; if both fail, the bug is in
// our wire-format emission.
func TestRoundTripParseable(t *testing.T) {
	outPath := filepath.Join(t.TempDir(), "x.kvei")
	b, err := NewFilterBuilder(100, outPath, false)
	if err != nil {
		t.Fatalf("NewFilterBuilder: %v", err)
	}
	b.DisableFsync()
	for i := uint64(0); i < 50; i++ {
		if err := b.AddHash(i * 0x9E3779B97F4A7C15); err != nil {
			t.Fatalf("AddHash: %v", err)
		}
	}
	if err := b.Build(); err != nil {
		t.Fatalf("Build: %v", err)
	}
	f, _, err := bloomfilter.ReadFile(outPath)
	if err != nil {
		t.Fatalf("bloomfilter.ReadFile: %v", err)
	}
	// Spot-check ContainsHash on values we inserted.
	for i := uint64(0); i < 50; i++ {
		if !f.ContainsHash(i * 0x9E3779B97F4A7C15) {
			t.Errorf("ContainsHash(%d) = false; want true", i)
		}
	}
}

// prefix returns the first n bytes of b, or all of b if len(b) < n.
// Used for compact error messages on byte-mismatch.
func prefix(b []byte, n int) []byte {
	if len(b) < n {
		return b
	}
	return b[:n]
}
