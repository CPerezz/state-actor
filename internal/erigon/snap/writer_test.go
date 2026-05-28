package snap

import (
	"context"
	"os"
	"path/filepath"
	"testing"
)

// TestWriterRoundTrip exercises NewWriter + WriteDomain end-to-end for
// the value-domain accessor mix (BTree + Existence). It does NOT
// byte-compare against Erigon — that's individual primitives' job.
// What this test verifies is that snap composes them correctly:
//
//   - The right four files land at the right paths.
//   - salt-state.txt + erigondb.toml are written.
//   - re-opening NewWriter with the same Settings is idempotent.
func TestWriterRoundTrip(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, Settings{Seed: 42})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	// Confirm salt-state.txt + erigondb.toml landed.
	for _, want := range []string{
		filepath.Join(SnapshotsDir(dir), "salt-state.txt"),
		filepath.Join(SnapshotsDir(dir), "erigondb.toml"),
	} {
		if _, err := os.Stat(want); err != nil {
			t.Fatalf("expected file %s: %v", want, err)
		}
	}

	// Emit a tiny accounts domain: 8 ascending keys.
	entries := []DomainEntry{
		{Key: []byte{0x01, 0x02, 0x03}, Value: []byte{0xaa}},
		{Key: []byte{0x01, 0x02, 0x04}, Value: []byte{0xbb, 0xcc}},
		{Key: []byte{0x01, 0x02, 0x05}, Value: []byte{0xdd, 0xee, 0xff}},
		{Key: []byte{0x10, 0x20, 0x30}, Value: []byte{0x11}},
		{Key: []byte{0x10, 0x20, 0x31}, Value: []byte{0x22, 0x33}},
		{Key: []byte{0x10, 0x20, 0x32}, Value: []byte{0x44, 0x55, 0x66}},
		{Key: []byte{0xff, 0x00, 0x00}, Value: []byte{0x77}},
		{Key: []byte{0xff, 0x00, 0x01}, Value: []byte{0x88, 0x99}},
	}
	push := func(yield func(DomainEntry) bool) {
		for _, e := range entries {
			if !yield(e) {
				return
			}
		}
	}
	r := StepRange{From: 0, To: 256}
	if err := w.WriteDomain(context.Background(), DomainAccounts, r, uint64(len(entries)), push); err != nil {
		t.Fatalf("WriteDomain: %v", err)
	}

	// Confirm the three accounts files exist.
	domain := DomainDir(dir)
	for _, want := range []string{
		BuildDataFilename(domain, "v1.0", DomainAccounts, r),
		BuildBTreeFilename(domain, "v1.0", DomainAccounts, r),
		BuildExistenceFilename(domain, "v1.0", DomainAccounts, r),
	} {
		info, err := os.Stat(want)
		if err != nil {
			t.Fatalf("expected file %s: %v", want, err)
		}
		if info.Size() == 0 {
			t.Errorf("file %s is empty", want)
		}
	}

	// Idempotency: NewWriter against the same datadir with matching
	// settings should succeed and reuse the salt.
	w2, err := NewWriter(dir, Settings{Seed: 42, Salt: w.Salt()})
	if err != nil {
		t.Fatalf("NewWriter (re-open): %v", err)
	}
	defer w2.Close()
	if w2.Salt() != w.Salt() {
		t.Errorf("salt mismatch on re-open: %d vs %d", w2.Salt(), w.Salt())
	}

	// Mismatch: NewWriter with a different explicit Salt should error.
	if _, err := NewWriter(dir, Settings{Seed: 42, Salt: w.Salt() + 1}); err == nil {
		t.Errorf("NewWriter with mismatched salt: expected error, got nil")
	}
}

// TestWriterCommitmentDomain exercises the AccessorHashMap (.kvi
// via recsplit) path that's commitment-domain-only. The mix is
// HashMap + Existence (no BTree).
func TestWriterCommitmentDomain(t *testing.T) {
	dir := t.TempDir()
	w, err := NewWriter(dir, Settings{Seed: 42})
	if err != nil {
		t.Fatalf("NewWriter: %v", err)
	}
	defer w.Close()

	// 128 keys is enough to exercise the bucket/recursion path
	// (BucketSize=100 default → 2 buckets).
	const n = 128
	entries := make([]DomainEntry, n)
	for i := range entries {
		k := []byte{byte(i >> 8), byte(i), 0x55, 0xaa}
		entries[i] = DomainEntry{Key: k, Value: []byte{byte(i)}}
	}
	push := func(yield func(DomainEntry) bool) {
		for _, e := range entries {
			if !yield(e) {
				return
			}
		}
	}
	r := StepRange{From: 0, To: 256}
	if err := w.WriteDomain(context.Background(), DomainCommitment, r, n, push); err != nil {
		t.Fatalf("WriteDomain(Commitment): %v", err)
	}

	domain := DomainDir(dir)
	for _, want := range []string{
		BuildDataFilename(domain, "v1.0", DomainCommitment, r),
		BuildHashMapFilename(domain, "v1.0", DomainCommitment, r),
		BuildExistenceFilename(domain, "v1.0", DomainCommitment, r),
	} {
		info, err := os.Stat(want)
		if err != nil {
			t.Fatalf("expected commitment file %s: %v", want, err)
		}
		if info.Size() == 0 {
			t.Errorf("file %s is empty", want)
		}
	}
	// .bt MUST NOT exist for commitment (per Verifier B's per-domain mix).
	btPath := BuildBTreeFilename(domain, "v1.0", DomainCommitment, r)
	if _, err := os.Stat(btPath); err == nil {
		t.Errorf("commitment domain should not emit .bt; found %s", btPath)
	}
}

func TestFilenameTemplate(t *testing.T) {
	r := StepRange{From: 0, To: 256}
	want := filepath.Join("/tmp", "v1.0-accounts.0-256.kv")
	if got := BuildDataFilename("/tmp", "v1.0", DomainAccounts, r); got != want {
		t.Errorf("BuildDataFilename = %q, want %q", got, want)
	}
	want = filepath.Join("/tmp", "v1.0-commitment.0-256.kvi")
	if got := BuildHashMapFilename("/tmp", "v1.0", DomainCommitment, r); got != want {
		t.Errorf("BuildHashMapFilename = %q, want %q", got, want)
	}
}

func TestDeriveSaltFromSeedDeterministic(t *testing.T) {
	if DeriveSaltFromSeed(42) != DeriveSaltFromSeed(42) {
		t.Error("DeriveSaltFromSeed not deterministic")
	}
	if DeriveSaltFromSeed(42) == DeriveSaltFromSeed(43) {
		t.Error("DeriveSaltFromSeed returns same salt for different seeds")
	}
}

func TestDefaultAccessorMaskPerDomain(t *testing.T) {
	cases := map[Domain]AccessorMask{
		DomainAccounts:   AccessorBTree | AccessorExistence,
		DomainStorage:    AccessorBTree | AccessorExistence,
		DomainCode:       AccessorBTree | AccessorExistence,
		DomainCommitment: AccessorHashMap | AccessorExistence,
	}
	for d, want := range cases {
		if got := DefaultAccessorMask(d); got != want {
			t.Errorf("DefaultAccessorMask(%v) = %#x, want %#x", d, got, want)
		}
	}
}
