package eliasfano

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

// fixture mirrors the JSON schema produced by
// upstream Erigon's reference eliasfano encoder.
type fixture struct {
	Label       string   `json:"label"`
	Inputs      []uint64 `json:"inputs"`
	ExpectedHex string   `json:"expected_hex"`
}

// TestGoldenAgainstErigon is the load-bearing byte-equality test:
// for every fixture captured by running Erigon's eliasfano32 writer
// (see upstream Erigon's reference eliasfano encoder), our pure-Go
// Builder must produce byte-identical output.
//
// This is the cross-verify check Architect B's "own the format" design
// hinges on — if any byte diverges, our writer will produce
// Erigon-unreadable .ef / embedded-EF-in-.kvi files at runtime.
//
// The golden fixtures are committed under testdata/; they were captured
// from upstream Erigon v3.4.2's reference Elias-Fano encoder.
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
			// Derive maxOffset from inputs (largest value).
			var maxOff uint64
			for _, v := range f.Inputs {
				if v > maxOff {
					maxOff = v
				}
			}
			// Special-case: sparse_8_max1M fixture used a hardcoded
			// maxOffset=1_000_000. Detect by label so the test config
			// stays in sync with the fixture generator.
			if f.Label == "sparse_8_max1M" {
				maxOff = 1_000_000
			}
			b, err := New(uint64(len(f.Inputs)), maxOff)
			if err != nil {
				t.Fatalf("New(%d, %d): %v", len(f.Inputs), maxOff, err)
			}
			for _, v := range f.Inputs {
				if err := b.AddOffset(v); err != nil {
					t.Fatalf("AddOffset(%d): %v", v, err)
				}
			}
			b.Build()
			got := b.AppendBytes(nil)
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
				t.Fatalf("byte mismatch for %s: first differs at byte %d (got=%x want=%x); lengths got=%d want=%d",
					f.Label, firstDiff, got, want, len(got), len(want))
			}
		})
	}
}
