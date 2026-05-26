package seg

import (
	"bytes"
	"context"
	"encoding/hex"
	"encoding/json"
	"errors"
	"os"
	"path/filepath"
	"testing"
)

// TestRoundTripBasic writes a 10-word .kv file, then reads it back via
// Iterate, asserting (1) every key/value matches, (2) byte offsets are
// strictly increasing, (3) the wordsCount/emptyCount header values
// match what was written.
func TestRoundTripBasic(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "test.kv")
	c, err := NewCompressor(out, dir, DefaultConfig())
	if err != nil {
		t.Fatalf("NewCompressor: %v", err)
	}
	defer c.Close()

	pairs := [][2][]byte{
		{[]byte("key01"), []byte("value-one")},
		{[]byte("key02"), []byte("value-two-longer")},
		{[]byte("key03"), []byte("v3")},
		{[]byte("key04"), []byte("value-four")},
		{[]byte("k5"), []byte("v5")},
	}
	for _, kv := range pairs {
		if err := c.AddWord(kv[0]); err != nil {
			t.Fatalf("AddWord key: %v", err)
		}
		if err := c.AddWord(kv[1]); err != nil {
			t.Fatalf("AddWord val: %v", err)
		}
	}
	if err := c.Compress(); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}

	info, err := os.Stat(out)
	if err != nil {
		t.Fatalf("stat output: %v", err)
	}
	if info.Size() < 32 {
		t.Fatalf("output too small: %d bytes", info.Size())
	}

	d, err := NewDecompressor(out)
	if err != nil {
		t.Fatalf("NewDecompressor: %v", err)
	}
	defer d.Close()
	if got, want := d.Count(), uint64(len(pairs)*2); got != want {
		t.Errorf("Count: got %d, want %d", got, want)
	}
	if got, want := d.EmptyCount(), uint64(0); got != want {
		t.Errorf("EmptyCount: got %d, want %d", got, want)
	}

	var lastValOff uint64
	i := 0
	for entry, err := range d.Iterate(context.Background()) {
		if err != nil {
			t.Fatalf("Iterate yielded error at idx %d: %v", i, err)
		}
		if i >= len(pairs) {
			t.Fatalf("Iterate yielded more entries than expected; got idx=%d", i)
		}
		if !bytes.Equal(entry.Key, pairs[i][0]) {
			t.Errorf("key[%d]: got %q, want %q", i, entry.Key, pairs[i][0])
		}
		if !bytes.Equal(entry.Value, pairs[i][1]) {
			t.Errorf("val[%d]: got %q, want %q", i, entry.Value, pairs[i][1])
		}
		if entry.KeyOffset >= entry.ValueOffset {
			t.Errorf("entry[%d]: KeyOffset %d should be < ValueOffset %d",
				i, entry.KeyOffset, entry.ValueOffset)
		}
		if i > 0 && entry.KeyOffset <= lastValOff {
			t.Errorf("entry[%d]: KeyOffset %d should be > previous valOff %d",
				i, entry.KeyOffset, lastValOff)
		}
		lastValOff = entry.ValueOffset
		i++
	}
	if i != len(pairs) {
		t.Errorf("Iterate yielded %d entries, want %d", i, len(pairs))
	}
}

// TestEmptyWordsCount verifies that adding empty-byte-value words is
// counted in emptyCount and round-trips correctly.
func TestEmptyWordsCount(t *testing.T) {
	dir := t.TempDir()
	out := filepath.Join(dir, "empty.kv")
	c, err := NewCompressor(out, dir, DefaultConfig())
	if err != nil {
		t.Fatalf("NewCompressor: %v", err)
	}
	defer c.Close()
	// Two pairs, each with an empty value.
	pairs := [][2][]byte{
		{[]byte("k1"), nil},
		{[]byte("k2"), []byte{}},
	}
	for _, kv := range pairs {
		if err := c.AddWord(kv[0]); err != nil {
			t.Fatalf("AddWord: %v", err)
		}
		if err := c.AddWord(kv[1]); err != nil {
			t.Fatalf("AddWord: %v", err)
		}
	}
	if err := c.Compress(); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	c.Close()
	d, err := NewDecompressor(out)
	if err != nil {
		t.Fatalf("NewDecompressor: %v", err)
	}
	defer d.Close()
	if got := d.EmptyCount(); got != 2 {
		t.Errorf("EmptyCount: got %d, want 2", got)
	}
}

// TestPatternCoverRejected verifies that requesting CompressKeys or
// CompressVals returns an explicit error (v1 only ships CompressNone).
func TestPatternCoverRejected(t *testing.T) {
	dir := t.TempDir()
	cfg := DefaultConfig()
	cfg.Compression = CompressKeys
	_, err := NewCompressor(filepath.Join(dir, "k.kv"), dir, cfg)
	if !errors.Is(err, ErrPatternCoverUnsupported) {
		t.Fatalf("got err %v, want ErrPatternCoverUnsupported", err)
	}
	cfg.Compression = CompressVals
	_, err = NewCompressor(filepath.Join(dir, "v.kv"), dir, cfg)
	if !errors.Is(err, ErrPatternCoverUnsupported) {
		t.Fatalf("got err %v, want ErrPatternCoverUnsupported", err)
	}
}

// TestCloseAfterCompress verifies multiple Close calls are safe.
func TestCloseAfterCompress(t *testing.T) {
	dir := t.TempDir()
	c, err := NewCompressor(filepath.Join(dir, "x.kv"), dir, DefaultConfig())
	if err != nil {
		t.Fatalf("NewCompressor: %v", err)
	}
	if err := c.AddWord([]byte("k")); err != nil {
		t.Fatalf("AddWord: %v", err)
	}
	if err := c.AddWord([]byte("v")); err != nil {
		t.Fatalf("AddWord: %v", err)
	}
	if err := c.Compress(); err != nil {
		t.Fatalf("Compress: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close 1: %v", err)
	}
	if err := c.Close(); err != nil {
		t.Fatalf("Close 2 (idempotent): %v", err)
	}
}

// fixture mirrors the schema produced by
// internal/erigon/_fixtures/cmd/seg/main.go.
type fixture struct {
	Label         string     `json:"label"`
	Words         [][]string `json:"words"` // list of (key_hex, value_hex) pairs
	ExpectedKvHex string     `json:"expected_kv_hex"`
}

// TestGoldenAgainstErigon is the load-bearing byte-equality test:
// for every fixture captured by running Erigon's seg.Compressor (see
// internal/erigon/_fixtures/cmd/seg/main.go), our pure-Go Compressor
// must produce byte-identical output. If the golden file is missing
// (oracle host not provisioned yet), the test is skipped with a
// regeneration hint instead of failing — matching the eliasfano /
// account pattern.
//
// Regenerate via:
//
//	cd internal/erigon/_fixtures
//	go run -tags erigon_gen ./cmd/seg \
//	    --out=../../seg/testdata/erigon_golden.json
func TestGoldenAgainstErigon(t *testing.T) {
	path := filepath.Join("testdata", "erigon_golden.json")
	data, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			t.Skip("seg/testdata/erigon_golden.json missing; regenerate via _fixtures/cmd/seg")
		}
		t.Fatalf("read %s: %v", path, err)
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
			want, err := hex.DecodeString(f.ExpectedKvHex)
			if err != nil {
				t.Fatalf("decode expected hex: %v", err)
			}
			dir := t.TempDir()
			outPath := filepath.Join(dir, f.Label+".kv")
			c, err := NewCompressor(outPath, dir, DefaultConfig())
			if err != nil {
				t.Fatalf("NewCompressor: %v", err)
			}
			defer c.Close()
			for i, pair := range f.Words {
				if len(pair) != 2 {
					t.Fatalf("fixture %s: word %d has %d fields, want 2", f.Label, i, len(pair))
				}
				k, err := hex.DecodeString(pair[0])
				if err != nil {
					t.Fatalf("fixture %s: decode key %d hex: %v", f.Label, i, err)
				}
				v, err := hex.DecodeString(pair[1])
				if err != nil {
					t.Fatalf("fixture %s: decode val %d hex: %v", f.Label, i, err)
				}
				if err := c.AddWord(k); err != nil {
					t.Fatalf("AddWord key %d: %v", i, err)
				}
				if err := c.AddWord(v); err != nil {
					t.Fatalf("AddWord val %d: %v", i, err)
				}
			}
			if err := c.Compress(); err != nil {
				t.Fatalf("Compress: %v", err)
			}
			if err := c.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}
			got, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("read output: %v", err)
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
				t.Fatalf("byte mismatch for %s: first differs at byte %d\n  got len=%d %x\n  want len=%d %x",
					f.Label, firstDiff, len(got), got, len(want), want)
			}
		})
	}
}
