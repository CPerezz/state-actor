//go:build erigon_gen

// Command erigon-fixture-seg regenerates byte-equality golden fixtures
// for the pure-Go internal/erigon/seg writer by running Erigon's
// reference seg.Compressor on fixed corpora and writing the result to
// a JSON file.
//
// Each fixture is a (label, words, expected_kv_hex) triple. The state-
// actor-side test at internal/erigon/seg/seg_test.go loads this JSON
// and asserts bytes.Equal between Erigon's bytes and our pure-Go output.
//
// Run:
//
//	cd internal/erigon/_fixtures
//	go run -tags erigon_gen ./cmd/seg \
//	    --out=../../seg/testdata/erigon_golden.json
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/rand/v2"
	"os"
	"path/filepath"

	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/seg"
)

type fixture struct {
	Label         string     `json:"label"`
	Words         [][]string `json:"words"` // list of (key_hex, value_hex)
	ExpectedKvHex string     `json:"expected_kv_hex"`
}

func main() {
	out := flag.String("out", "", "output JSON file path (required)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: seg --out=<path>")
		os.Exit(2)
	}

	// Curated input corpora. Each label corresponds to an algorithmic
	// regime. With Erigon's default MinPatternScore=1024, small inputs
	// (≤1K words) hit the no-pattern fast path; we don't include large
	// pattern-heavy inputs in v1.
	corpora := []struct {
		label string
		words [][2][]byte // (key, value) pairs
	}{
		{
			label: "ten_pairs_short",
			words: deterministicPairs(10, 5, 16, 42),
		},
		{
			label: "ten_pairs_with_empty_values",
			words: append(
				deterministicPairs(8, 5, 12, 17),
				[2][]byte{[]byte("emptyk1"), nil},
				[2][]byte{[]byte("emptyk2"), {}},
			),
		},
		{
			label: "hundred_pairs",
			words: deterministicPairs(100, 8, 32, 123),
		},
		{
			label: "thousand_pairs",
			words: deterministicPairs(1000, 5, 64, 99),
		},
	}

	fixtures := make([]fixture, 0, len(corpora))
	ctx := context.Background()
	logger := log.New() // discards by default

	for _, c := range corpora {
		tmpDir, err := os.MkdirTemp("", "seg-fixture-"+c.label+"-")
		if err != nil {
			fail("mkdir tmp:", err)
		}
		outFile := filepath.Join(tmpDir, c.label+".kv")
		cfg := seg.DefaultCfg
		cfg.Workers = 1 // ensure deterministic output (parallel path may interleave)
		comp, err := seg.NewCompressor(ctx, "fixture-"+c.label, outFile, tmpDir, cfg, log.LvlError, logger)
		if err != nil {
			fail("NewCompressor:", err)
		}
		for _, kv := range c.words {
			if err := comp.AddWord(kv[0]); err != nil {
				fail("AddWord key:", err)
			}
			if err := comp.AddWord(kv[1]); err != nil {
				fail("AddWord val:", err)
			}
		}
		if err := comp.Compress(); err != nil {
			fail("Compress:", err)
		}
		comp.Close()

		blob, err := os.ReadFile(outFile)
		if err != nil {
			fail("read output:", err)
		}

		// Encode words as pairs of hex strings for the JSON.
		wordsHex := make([][]string, len(c.words))
		for i, kv := range c.words {
			wordsHex[i] = []string{
				hex.EncodeToString(kv[0]),
				hex.EncodeToString(kv[1]),
			}
		}

		fixtures = append(fixtures, fixture{
			Label:         c.label,
			Words:         wordsHex,
			ExpectedKvHex: hex.EncodeToString(blob),
		})

		_ = os.RemoveAll(tmpDir)
	}

	data, err := json.MarshalIndent(fixtures, "", "  ")
	if err != nil {
		fail("marshal:", err)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fail("write:", err)
	}
	fmt.Fprintln(os.Stderr, "wrote", *out, "with", len(fixtures), "fixtures")
}

// deterministicPairs generates n (key, value) pairs using a seeded RNG
// so the fixture is byte-stable across regenerations. Key/value
// lengths are uniform random in [minLen, maxLen].
func deterministicPairs(n, minLen, maxLen int, seed uint64) [][2][]byte {
	rng := rand.New(rand.NewPCG(seed, seed+1))
	out := make([][2][]byte, n)
	for i := 0; i < n; i++ {
		out[i] = [2][]byte{
			randBytes(rng, minLen, maxLen),
			randBytes(rng, minLen, maxLen),
		}
	}
	return out
}

func randBytes(rng *rand.Rand, minLen, maxLen int) []byte {
	n := minLen + rng.IntN(maxLen-minLen+1)
	b := make([]byte, n)
	for i := range b {
		b[i] = byte(rng.IntN(256))
	}
	return b
}

func fail(prefix string, err error) {
	fmt.Fprintln(os.Stderr, prefix, err)
	os.Exit(1)
}
