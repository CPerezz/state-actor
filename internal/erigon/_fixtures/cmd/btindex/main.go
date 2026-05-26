//go:build erigon_gen

// Command erigon-fixture-btindex regenerates byte-equality golden
// fixtures for the pure-Go internal/erigon/btindex writer by running
// Erigon's reference BtIndexWriter on a fixed corpus of synthetic
// (key, offset) inputs and writing the result to a JSON file.
//
// We drive Erigon's writer directly (rather than going through
// BuildBtreeIndexWithDecompressor + a .kv file) because we control
// the input order and want fixture determinism. The b0 first-byte
// sentinel — normally applied by BuildBtreeIndexWithDecompressor at
// btree_index.go:424-431 — is replicated here in pure form so our
// `iw.AddKey(key, off, keep)` calls match the production keep-decision
// flow exactly. State-actor's own writer encapsulates the same b0
// logic inside AddKey (since its signature drops the `keep` param).
//
// Run:
//
//	cd internal/erigon/_fixtures
//	go run -tags erigon_gen ./cmd/btindex \
//	    --out=../../btindex/testdata/erigon_golden.json
package main

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"

	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/datastruct/btindex"
)

// fixture mirrors the JSON schema consumed by
// internal/erigon/btindex/btindex_test.go.
type fixture struct {
	Label string `json:"label"`
	M     uint16 `json:"m"`
	// Input is a list of (key_hex, offset) pairs in monotonic
	// offset order — exactly what AddKey expects.
	Input       []kvPair `json:"input"`
	ExpectedHex string   `json:"expected_hex"`
}

type kvPair struct {
	KeyHex string `json:"key_hex"`
	Offset uint64 `json:"offset"`
}

func main() {
	out := flag.String("out", "", "output JSON file path (required)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: btindex --out=<path>")
		os.Exit(2)
	}

	logger := log.New()

	// Curated input shapes. Each exercises a different size/leaf-count
	// boundary in the BTree writer.
	corpora := []struct {
		label    string
		m        uint16
		keyCount int
		keyGen   func(i int) []byte
		offGen   func(i int) uint64
	}{
		{
			// Edge: 2 keys with M=256. nodeCount should be 1 (only
			// the first key is kept; second key is at di=1 which is
			// not a multiple of M).
			label:    "tiny_2",
			m:        256,
			keyCount: 2,
			keyGen:   deterministicKey(32),
			offGen:   linearOffset(100),
		},
		{
			// One full leaf at M=256: 100 keys, nodeCount should be 1
			// (di=0 only).
			label:    "single_leaf_100",
			m:        256,
			keyCount: 100,
			keyGen:   deterministicKey(32),
			offGen:   linearOffset(64),
		},
		{
			// Crosses the M=256 boundary: 300 keys → kept at di=0, 256.
			// nodeCount should be 2.
			label:    "two_leaves_300",
			m:        256,
			keyCount: 300,
			keyGen:   deterministicKey(32),
			offGen:   linearOffset(48),
		},
		{
			// Multi-level: 1000 keys at M=256 → kept at di=0, 256, 512, 768.
			// nodeCount should be 4.
			label:    "multi_level_1000",
			m:        256,
			keyCount: 1000,
			keyGen:   deterministicKey(32),
			offGen:   linearOffset(40),
		},
		{
			// Smaller M to exercise more nodes per fixed key count.
			// 120 keys at M=4 → kept at di=0, 4, 8, ..., 116. nodeCount=30.
			label:    "small_m4_120",
			m:        4,
			keyCount: 120,
			keyGen:   deterministicKey(20),
			offGen:   linearOffset(32),
		},
	}

	tmp, err := os.MkdirTemp("", "btindex-fixture-*")
	if err != nil {
		fmt.Fprintln(os.Stderr, "mktemp:", err)
		os.Exit(1)
	}
	defer os.RemoveAll(tmp)

	fixtures := make([]fixture, 0, len(corpora))
	for _, c := range corpora {
		// Generate the input pairs.
		input := make([]kvPair, c.keyCount)
		for i := 0; i < c.keyCount; i++ {
			input[i] = kvPair{
				KeyHex: hex.EncodeToString(c.keyGen(i)),
				Offset: c.offGen(i),
			}
		}

		// Drive Erigon's BtIndexWriter directly.
		indexFile := filepath.Join(tmp, c.label+".bt")
		args := btindex.BtIndexWriterArgs{
			IndexFile: indexFile,
			TmpDir:    tmp,
			M:         uint64(c.m),
			KeyCount:  c.keyCount,
		}
		iw, err := btindex.NewBtIndexWriter(args, logger)
		if err != nil {
			fmt.Fprintln(os.Stderr, "NewBtIndexWriter:", err)
			os.Exit(1)
		}
		iw.DisableFsync() // tests don't need durability and CI runs faster

		// Mirror BuildBtreeIndexWithDecompressor's b0[] tracking so
		// `keep` is computed the same way state-actor's encapsulated
		// writer does. NOTE: only the keep flag at ordinal 0 actually
		// matters (writer overwrites keep at ordinal > 0) — but we
		// pass the truthful value for byte-for-byte parity.
		var b0 [256]bool
		for i, p := range input {
			keep := false
			keyBytes, decErr := hex.DecodeString(p.KeyHex)
			if decErr != nil {
				fmt.Fprintln(os.Stderr, "decode key:", decErr)
				os.Exit(1)
			}
			if !b0[keyBytes[0]] {
				b0[keyBytes[0]] = true
				keep = true
			}
			if err := iw.AddKey(keyBytes, p.Offset, keep); err != nil {
				fmt.Fprintln(os.Stderr, "AddKey", i, ":", err)
				os.Exit(1)
			}
		}
		if err := iw.Build(); err != nil {
			fmt.Fprintln(os.Stderr, "Build:", err)
			os.Exit(1)
		}
		iw.Close()

		// Read back the final file as bytes.
		expected, err := os.ReadFile(indexFile)
		if err != nil {
			fmt.Fprintln(os.Stderr, "read fixture:", err)
			os.Exit(1)
		}

		fixtures = append(fixtures, fixture{
			Label:       c.label,
			M:           c.m,
			Input:       input,
			ExpectedHex: hex.EncodeToString(expected),
		})

		fmt.Fprintf(os.Stderr, "  %s: keys=%d M=%d → %d bytes\n", c.label, c.keyCount, c.m, len(expected))
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

// deterministicKey returns a key-generator that produces `size`-byte
// keys derived from sha256(i). Top byte varies so the b0[] sentinel
// only fires at ordinal 0 (matching production where keys are
// pseudo-random / hashed addresses).
func deterministicKey(size int) func(int) []byte {
	return func(i int) []byte {
		var idxBuf [8]byte
		binary.BigEndian.PutUint64(idxBuf[:], uint64(i))
		sum := sha256.Sum256(idxBuf[:])
		// Encode i in BE in the last 8 bytes so keys are sortable by
		// i. (Production keys are sorted by hash, but for fixture
		// determinism we want di to track input order.)
		out := make([]byte, size)
		if size >= 8 {
			// Use the hash to fill prefix, but force a monotonically
			// non-decreasing prefix by writing the index in BE at the
			// front — guarantees stable order.
			binary.BigEndian.PutUint64(out[:8], uint64(i))
			if size > 8 {
				copy(out[8:], sum[:size-8])
			}
		} else {
			copy(out, sum[:size])
		}
		// Defensive: ensure non-empty (writer requires it).
		if bytes.Equal(out, make([]byte, size)) {
			out[0] = 0x01
		}
		return out
	}
}

// linearOffset returns an offset-generator producing offset = i*step.
// Monotonic and non-decreasing by construction.
func linearOffset(step uint64) func(int) uint64 {
	return func(i int) uint64 {
		return uint64(i) * step
	}
}
