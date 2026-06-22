package btindex

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

// fixture mirrors the JSON schema produced by
// upstream Erigon's reference btindex encoder.
type fixture struct {
	Label       string   `json:"label"`
	M           uint16   `json:"m"`
	Input       []kvPair `json:"input"`
	ExpectedHex string   `json:"expected_hex"`
}

type kvPair struct {
	KeyHex string `json:"key_hex"`
	Offset uint64 `json:"offset"`
}

// TestGoldenAgainstErigon is the load-bearing byte-equality test:
// for every fixture captured by running Erigon's BtIndexWriter (see
// upstream Erigon's reference btindex encoder), our pure-Go Writer
// must produce a byte-identical `.bt` file.
//
// The golden fixtures are committed under testdata/; they were captured
// from upstream Erigon v3.4.2's reference .bt writer.
func TestGoldenAgainstErigon(t *testing.T) {
	path := filepath.Join("testdata", "erigon_golden.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (golden is committed under testdata/)", path, err)
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

			tmpDir := t.TempDir()
			outPath := filepath.Join(tmpDir, f.Label+".bt")

			w, err := New(Args{
				KeyCount:  len(f.Input),
				M:         f.M,
				TmpDir:    tmpDir,
				IndexFile: outPath,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			for i, kv := range f.Input {
				keyBytes, decErr := hex.DecodeString(kv.KeyHex)
				if decErr != nil {
					t.Fatalf("decode key %d: %v", i, decErr)
				}
				if addErr := w.AddKey(keyBytes, kv.Offset); addErr != nil {
					t.Fatalf("AddKey %d: %v", i, addErr)
				}
			}
			if err := w.Build(context.Background()); err != nil {
				t.Fatalf("Build: %v", err)
			}
			if err := w.Close(); err != nil {
				t.Fatalf("Close: %v", err)
			}

			got, err := os.ReadFile(outPath)
			if err != nil {
				t.Fatalf("read produced file: %v", err)
			}
			if !bytes.Equal(got, want) {
				// Pinpoint the first divergent byte for debug ergonomics.
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
				t.Fatalf("byte mismatch for %s: first differs at byte %d; lengths got=%d want=%d\n  got=%x\n  want=%x",
					f.Label, firstDiff, len(got), len(want), got, want)
			}
		})
	}
}

// TestEmptyIndex ensures KeyCount=0 produces a zero-byte file (which
// Erigon's reader handles at btree_index.go:489-491).
func TestEmptyIndex(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "empty.bt")
	w, err := New(Args{KeyCount: 0, M: 256, TmpDir: tmpDir, IndexFile: outPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := w.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	info, err := os.Stat(outPath)
	if err != nil {
		t.Fatalf("stat: %v", err)
	}
	if info.Size() != 0 {
		t.Errorf("empty index file should be 0 bytes, got %d", info.Size())
	}
}

// TestAddKeyAfterBuild ensures the post-Build guard.
func TestAddKeyAfterBuild(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "x.bt")
	w, err := New(Args{KeyCount: 1, M: 256, TmpDir: tmpDir, IndexFile: outPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.AddKey([]byte{0x01, 0x02}, 0); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if err := w.Build(context.Background()); err != nil {
		t.Fatalf("Build: %v", err)
	}
	if err := w.AddKey([]byte{0x03}, 10); !errors.Is(err, ErrAddKeyAfterBuild) {
		t.Errorf("AddKey after Build: want ErrAddKeyAfterBuild, got %v", err)
	}
}

// TestBuildTwice ensures Build is single-use.
func TestBuildTwice(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "x.bt")
	w, err := New(Args{KeyCount: 1, M: 256, TmpDir: tmpDir, IndexFile: outPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.AddKey([]byte{0x01}, 0); err != nil {
		t.Fatalf("AddKey: %v", err)
	}
	if err := w.Build(context.Background()); err != nil {
		t.Fatalf("Build first: %v", err)
	}
	if err := w.Build(context.Background()); !errors.Is(err, ErrAlreadyBuilt) {
		t.Errorf("Build twice: want ErrAlreadyBuilt, got %v", err)
	}
}

// TestAddKeyTooMany ensures KeyCount is a hard cap.
func TestAddKeyTooMany(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "x.bt")
	w, err := New(Args{KeyCount: 2, M: 256, TmpDir: tmpDir, IndexFile: outPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.AddKey([]byte{0x01}, 0); err != nil {
		t.Fatalf("AddKey 0: %v", err)
	}
	if err := w.AddKey([]byte{0x02}, 10); err != nil {
		t.Fatalf("AddKey 1: %v", err)
	}
	if err := w.AddKey([]byte{0x03}, 20); !errors.Is(err, ErrTooManyKeys) {
		t.Errorf("third AddKey on KeyCount=2: want ErrTooManyKeys, got %v", err)
	}
}

// TestNonMonotonicOffset ensures we enforce the EF builder's
// monotonicity invariant on input.
func TestNonMonotonicOffset(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "x.bt")
	w, err := New(Args{KeyCount: 3, M: 256, TmpDir: tmpDir, IndexFile: outPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.AddKey([]byte{0x01}, 100); err != nil {
		t.Fatalf("AddKey 0: %v", err)
	}
	if err := w.AddKey([]byte{0x02}, 99); !errors.Is(err, ErrNonMonotonicOffset) {
		t.Errorf("backwards offset: want ErrNonMonotonicOffset, got %v", err)
	}
}

// TestCountMismatch ensures Build fails if fewer keys than declared
// were written.
func TestCountMismatch(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "x.bt")
	w, err := New(Args{KeyCount: 3, M: 256, TmpDir: tmpDir, IndexFile: outPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.AddKey([]byte{0x01}, 0); err != nil {
		t.Fatalf("AddKey 0: %v", err)
	}
	if err := w.Build(context.Background()); !errors.Is(err, ErrCountMismatch) {
		t.Errorf("Build with 1/3 keys: want ErrCountMismatch, got %v", err)
	}
}

// TestEmptyKey ensures the b0 sentinel doesn't panic on zero-length
// input.
func TestEmptyKey(t *testing.T) {
	tmpDir := t.TempDir()
	outPath := filepath.Join(tmpDir, "x.bt")
	w, err := New(Args{KeyCount: 1, M: 256, TmpDir: tmpDir, IndexFile: outPath})
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := w.AddKey([]byte{}, 0); !errors.Is(err, ErrEmptyKey) {
		t.Errorf("empty key: want ErrEmptyKey, got %v", err)
	}
}

// TestNodeEncodeDecodeRoundTrip exercises the internal Node codec
// (encodeListNodes / decodeListNodes) against Erigon's wire layout.
func TestNodeEncodeDecodeRoundTrip(t *testing.T) {
	nodes := []node{
		{key: []byte("alpha"), di: 0},
		{key: []byte("beta"), di: 256},
		{key: []byte("gamma-with-longer-name"), di: 512},
	}
	var buf bytes.Buffer
	if err := encodeListNodes(nodes, &buf); err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, consumed, err := decodeListNodes(buf.Bytes())
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if consumed != buf.Len() {
		t.Errorf("consumed %d, expected %d", consumed, buf.Len())
	}
	if len(got) != len(nodes) {
		t.Fatalf("len mismatch: %d vs %d", len(got), len(nodes))
	}
	for i := range nodes {
		if got[i].di != nodes[i].di {
			t.Errorf("node[%d].di: got %d want %d", i, got[i].di, nodes[i].di)
		}
		if !bytes.Equal(got[i].key, nodes[i].key) {
			t.Errorf("node[%d].key: got %x want %x", i, got[i].key, nodes[i].key)
		}
	}
}

// TestInvalidArgs ensures New rejects malformed Args.
func TestInvalidArgs(t *testing.T) {
	if _, err := New(Args{KeyCount: -1, IndexFile: "/tmp/x.bt"}); err == nil {
		t.Error("KeyCount=-1: expected error")
	}
	if _, err := New(Args{KeyCount: 5, IndexFile: ""}); err == nil {
		t.Error("empty IndexFile: expected error")
	}
	// M=0 → defaults to DefaultBtreeM, not an error.
	w, err := New(Args{KeyCount: 1, M: 0, IndexFile: "/tmp/x.bt"})
	if err != nil {
		t.Errorf("M=0 default: unexpected error %v", err)
	}
	if w != nil && w.args.M != DefaultBtreeM {
		t.Errorf("M=0 should default to DefaultBtreeM, got %d", w.args.M)
	}
}
