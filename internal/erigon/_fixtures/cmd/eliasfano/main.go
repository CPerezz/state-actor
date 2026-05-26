//go:build erigon_gen

// Command erigon-fixture-eliasfano regenerates byte-equality golden
// fixtures for the pure-Go internal/erigon/eliasfano writer by running
// Erigon's reference encoder on a fixed corpus and writing the result
// to a JSON file.
//
// Each entry in the JSON output is a (label, inputs, expected_hex)
// triple. The state-actor-side test at
// internal/erigon/eliasfano/eliasfano_test.go loads this JSON and
// asserts bytes.Equal between Erigon's bytes and our pure-Go output.
//
// Run:
//
//	cd internal/erigon/_fixtures
//	go run -tags erigon_gen ./cmd/eliasfano \
//	    --out=../../eliasfano/testdata/golden.json
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"

	"github.com/erigontech/erigon/db/recsplit/eliasfano32"
)

type fixture struct {
	Label       string   `json:"label"`
	Inputs      []uint64 `json:"inputs"`
	ExpectedHex string   `json:"expected_hex"`
}

func main() {
	out := flag.String("out", "", "output JSON file path (required)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: eliasfano --out=<path>")
		os.Exit(2)
	}

	// Curated input corpora. Each label corresponds to a specific
	// algorithmic boundary the encoder must handle.
	corpora := []struct {
		label   string
		inputs  []uint64
		maxOver uint64 // explicit maxOffset override (0 → auto-derive from max)
	}{
		// Erigon's canonical TestEliasFano fixture (db/recsplit/eliasfano32/elias_fano_test.go:496)
		{"erigon_canonical_19", []uint64{1, 4, 6, 8, 10, 14, 16, 19, 22, 34, 37, 39, 41, 43, 48, 51, 54, 58, 62}, 0},
		// Single-element edge case.
		{"singleton_zero", []uint64{0}, 0},
		// Larger, dense.
		{"dense_64", makeDense(64), 0},
		// Sparse, larger max.
		{"sparse_8_max1M", []uint64{1, 100, 1000, 10_000, 100_000, 500_000, 800_000, 999_999}, 1_000_000},
	}

	fixtures := make([]fixture, 0, len(corpora))
	for _, c := range corpora {
		mx := c.maxOver
		if mx == 0 {
			for _, v := range c.inputs {
				if v > mx {
					mx = v
				}
			}
		}
		ef := eliasfano32.NewEliasFano(uint64(len(c.inputs)), mx)
		for _, v := range c.inputs {
			ef.AddOffset(v)
		}
		ef.Build()
		bytes := ef.AppendBytes(nil)
		fixtures = append(fixtures, fixture{
			Label:       c.label,
			Inputs:      c.inputs,
			ExpectedHex: hex.EncodeToString(bytes),
		})
	}

	data, err := json.MarshalIndent(fixtures, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "wrote", *out)
}

func makeDense(n int) []uint64 {
	out := make([]uint64, n)
	for i := range out {
		out[i] = uint64(i)
	}
	return out
}
