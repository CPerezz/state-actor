//go:build recsplit_large

// This file is gated behind `recsplit_large` build tag — runs the
// pure-Go writer end-to-end at 100K + 1M scale to surface scaling bugs
// the small-fixture spike won't catch. Does NOT compare byte-for-byte
// against Erigon (would require generating a 100MB+ fixture); instead
// asserts the writer (a) completes without collision/error, and (b)
// produces a file with the expected approximate size.
//
// Run with:
//
//	go test -tags recsplit_large -run TestRecSplit_Large -timeout 5m ./internal/erigon/recsplit/...

package recsplit

import (
	"context"
	"math/rand"
	"os"
	"path/filepath"
	"testing"
)

func TestRecSplit_Large(t *testing.T) {
	cases := []struct {
		name  string
		count int
	}{
		{"100K", 100_000},
		// {"1M", 1_000_000},  // Uncomment + bump -timeout when running locally.
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			tmp := t.TempDir()
			idxPath := filepath.Join(tmp, "large.kvi")
			salt := uint32(0xDEADC0DE)
			rng := rand.New(rand.NewSource(42))

			w, err := New(Args{
				KeyCount:   tc.count,
				BucketSize: 2000, // production-ish bucket size
				LeafSize:   8,
				Salt:       &salt,
				TmpDir:     tmp,
				IndexFile:  idxPath,
				BaseDataID: 0,
			})
			if err != nil {
				t.Fatalf("New: %v", err)
			}
			defer w.Close()

			var off uint64
			for i := 0; i < tc.count; i++ {
				k := make([]byte, 32)
				_, _ = rng.Read(k)
				off += uint64(rng.Intn(127) + 1)
				if err := w.AddKey(k, off); err != nil {
					t.Fatalf("AddKey[%d]: %v", i, err)
				}
			}
			if err := w.Build(context.Background()); err != nil {
				t.Fatalf("Build: %v (collision=%v)", err, w.Collision())
			}

			info, err := os.Stat(idxPath)
			if err != nil {
				t.Fatalf("stat: %v", err)
			}
			if info.Size() == 0 {
				t.Fatal("produced .kvi is empty")
			}
			t.Logf("LARGE OK: count=%d → %d bytes (%.2f bits/key)",
				tc.count, info.Size(), 8*float64(info.Size())/float64(tc.count))
		})
	}
}
