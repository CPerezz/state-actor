//go:build erigon_gen

// Command erigon-fixture-recsplit regenerates byte-equality golden
// fixtures for the pure-Go internal/erigon/recsplit writer by running
// Erigon's reference RecSplit.Build() on a fixed corpus of keys + offsets
// and writing the resulting .kvi bytes to a JSON file.
//
// The state-actor-side spike test at
// internal/erigon/recsplit/spike_test.go loads this JSON, calls the
// pure-Go writer with the SAME (keys, offsets, salt, baseDataID) tuple,
// and asserts bytes.Equal between Erigon's bytes and our output.
//
// Run:
//
//	cd internal/erigon/_fixtures
//	go run -tags erigon_gen ./cmd/recsplit \
//	    --out=../../recsplit/testdata/spike_100.json
//
// The build tag isolates this from the main module — the production
// state-actor build path does NOT pull github.com/erigontech/erigon.
package main

import (
	"context"
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"math/rand"
	"os"
	"path/filepath"

	"github.com/erigontech/erigon/common/log/v3"
	"github.com/erigontech/erigon/db/recsplit"
)

// fixture mirrors the JSON schema read by the spike test on the
// state-actor side. Keys/offsets are emitted as hex/uint64 so the test
// can deterministically reconstruct the same input sequence.
type fixtureEntry struct {
	KeyHex string `json:"key_hex"`
	Offset uint64 `json:"offset"`
}

type fixture struct {
	Label       string         `json:"label"`
	KeyCount    int            `json:"key_count"`
	BucketSize  int            `json:"bucket_size"`
	LeafSize    uint16         `json:"leaf_size"`
	Salt        uint32         `json:"salt"`
	BaseDataID  uint64         `json:"base_data_id"`
	Enums       bool           `json:"enums"`
	Entries     []fixtureEntry `json:"entries"`
	ExpectedHex string         `json:"expected_hex"`
}

func main() {
	out := flag.String("out", "", "output JSON file path (required)")
	count := flag.Int("count", 100, "number of keys to generate")
	seed := flag.Int64("seed", 42, "PRNG seed for deterministic key generation")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: recsplit --out=<path> [--count=N] [--seed=S]")
		os.Exit(2)
	}

	tmpDir, err := os.MkdirTemp("", "recsplit-fixture-")
	if err != nil {
		die(err)
	}
	defer os.RemoveAll(tmpDir)

	// Generate `count` deterministic random 32-byte keys + ascending offsets
	// (offsets monotonically increasing because Enums mode requires it; we'll
	// run with Enums=false in spike so the constraint is relaxed but keep
	// monotonic for symmetry with production usage).
	rng := rand.New(rand.NewSource(*seed))
	entries := make([]fixtureEntry, *count)
	var off uint64
	for i := 0; i < *count; i++ {
		k := make([]byte, 32)
		_, _ = rng.Read(k)
		off += uint64(rng.Intn(127) + 1) // monotonic, small deltas
		entries[i] = fixtureEntry{
			KeyHex: hex.EncodeToString(k),
			Offset: off,
		}
	}

	// PINNED salt — must match what the state-actor side passes in.
	// 0xCAFEBABE picked because it's distinctive in xxd output.
	salt := uint32(0xCAFEBABE)
	const baseDataID uint64 = 1_000_000
	const bucketSize = 100
	const leafSize uint16 = 8

	idxPath := filepath.Join(tmpDir, "spike_100.kvi")

	logger := log.New()
	logger.SetHandler(log.DiscardHandler())

	rs, err := recsplit.NewRecSplit(recsplit.RecSplitArgs{
		KeyCount:   *count,
		BucketSize: bucketSize,
		LeafSize:   leafSize,
		Salt:       &salt,
		TmpDir:     tmpDir,
		IndexFile:  idxPath,
		BaseDataID: baseDataID,
		Enums:      false,
		NoFsync:    true,
	}, logger)
	if err != nil {
		die(fmt.Errorf("NewRecSplit: %w", err))
	}
	defer rs.Close()

	for _, e := range entries {
		k, err := hex.DecodeString(e.KeyHex)
		if err != nil {
			die(fmt.Errorf("decode key hex: %w", err))
		}
		if err := rs.AddKey(k, e.Offset); err != nil {
			die(fmt.Errorf("AddKey: %w", err))
		}
	}

	for {
		if err := rs.Build(context.Background()); err != nil {
			if rs.Collision() {
				fmt.Fprintf(os.Stderr, "fixture: collision at salt=%d, bumping...\n", rs.Salt())
				rs.ResetNextSalt()
				// Re-add keys after reset.
				for _, e := range entries {
					k, _ := hex.DecodeString(e.KeyHex)
					if err := rs.AddKey(k, e.Offset); err != nil {
						die(fmt.Errorf("re-AddKey after reset: %w", err))
					}
				}
				continue
			}
			die(fmt.Errorf("Build: %w", err))
		}
		// Save the final salt actually used (may have been bumped).
		salt = rs.Salt()
		break
	}

	// Read produced .kvi file as bytes.
	f, err := os.Open(idxPath)
	if err != nil {
		die(fmt.Errorf("open .kvi: %w", err))
	}
	defer f.Close()
	idxBytes, err := io.ReadAll(f)
	if err != nil {
		die(fmt.Errorf("read .kvi: %w", err))
	}

	fx := fixture{
		Label:       fmt.Sprintf("spike_%d", *count),
		KeyCount:    *count,
		BucketSize:  bucketSize,
		LeafSize:    leafSize,
		Salt:        salt,
		BaseDataID:  baseDataID,
		Enums:       false,
		Entries:     entries,
		ExpectedHex: hex.EncodeToString(idxBytes),
	}

	data, err := json.MarshalIndent(fx, "", "  ")
	if err != nil {
		die(fmt.Errorf("marshal: %w", err))
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		die(fmt.Errorf("write: %w", err))
	}
	fmt.Fprintf(os.Stderr, "wrote %s (%d keys, salt=0x%x, %d bytes)\n",
		*out, *count, salt, len(idxBytes))
}

func die(err error) {
	fmt.Fprintln(os.Stderr, "fixture-recsplit:", err)
	os.Exit(1)
}
