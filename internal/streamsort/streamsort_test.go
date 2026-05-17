package streamsort

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"testing"
)

// TestStoreSortsRandomInput puts 10 000 uniformly-random 32-byte keys
// (mirrors the per-entity workload — keys are keccak(slot_key) for the
// storage MPT) plus 32-byte values, then iterates and asserts:
//
//   - every Put landed in the iterator (no drops)
//   - the iterator yields keys in strictly ascending bytewise order
//   - each yielded value matches its key's Put value (no key→value drift)
//
// Doubles as a regression guard against drift in the tuning knobs in
// New (e.g. someone disabling the WAL flush hook and breaking the
// read-after-write contract).
func TestStoreSortsRandomInput(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	const N = 10_000
	r := rand.New(rand.NewPCG(0xdeadbeef, 0xcafe1234))

	type kv struct{ key, value [32]byte }
	entries := make([]kv, N)
	for i := range entries {
		var seed [16]byte
		binary.BigEndian.PutUint64(seed[0:], r.Uint64())
		binary.BigEndian.PutUint64(seed[8:], r.Uint64())
		entries[i].key = sha256.Sum256(append([]byte("k:"), seed[:]...))
		entries[i].value = sha256.Sum256(append([]byte("v:"), seed[:]...))
		if err := s.Put(entries[i].key[:], entries[i].value[:]); err != nil {
			t.Fatalf("Put[%d]: %v", i, err)
		}
	}

	want := make(map[[32]byte][32]byte, N)
	for _, e := range entries {
		want[e.key] = e.value
	}

	var (
		got     int
		prev    [32]byte
		hasPrev bool
	)
	iterErr := s.Iterate(func(k, v []byte) error {
		if len(k) != 32 || len(v) != 32 {
			return fmt.Errorf("unexpected len(k)=%d len(v)=%d", len(k), len(v))
		}
		if hasPrev && bytes.Compare(prev[:], k) >= 0 {
			return fmt.Errorf("not sorted: prev=%x current=%x", prev, k)
		}
		var kk [32]byte
		copy(kk[:], k)
		wantV, ok := want[kk]
		if !ok {
			return fmt.Errorf("yielded unknown key %x", k)
		}
		if !bytes.Equal(wantV[:], v) {
			return fmt.Errorf("value mismatch for key %x: got %x want %x", k, v, wantV)
		}
		delete(want, kk)
		copy(prev[:], k)
		hasPrev = true
		got++
		return nil
	})
	if iterErr != nil {
		t.Fatalf("Iterate: %v", iterErr)
	}
	if got != N {
		t.Errorf("yielded %d entries, want %d", got, N)
	}
	if len(want) != 0 {
		t.Errorf("Iterate dropped %d keys", len(want))
	}
}

// TestStorePutAfterClose verifies Put returns an error (rather than
// panicking) after Close.
func TestStorePutAfterClose(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Put([]byte("k"), []byte("v")); err == nil {
		t.Error("Put after Close: expected error, got nil")
	}
}

// TestStoreIterateAfterClose verifies Iterate returns an error after
// Close.
func TestStoreIterateAfterClose(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Iterate(func(_, _ []byte) error { return nil }); err == nil {
		t.Error("Iterate after Close: expected error, got nil")
	}
}

// TestStoreCloseIdempotent verifies double-Close returns nil.
func TestStoreCloseIdempotent(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Errorf("second Close: got %v, want nil", err)
	}
}

// TestStoreIterateYieldErrorPropagates verifies yield's error short-
// circuits iteration and is returned verbatim.
func TestStoreIterateYieldErrorPropagates(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	for i := 0; i < 100; i++ {
		k := []byte{byte(i)}
		v := []byte{byte(i)}
		if err := s.Put(k, v); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	sentinel := errors.New("stop now")
	gotErr := s.Iterate(func(_, _ []byte) error { return sentinel })
	if gotErr == nil || gotErr.Error() != sentinel.Error() {
		t.Errorf("Iterate: got %v, want %v", gotErr, sentinel)
	}
}

// TestStoreIterateRepeatable: two consecutive Iterate calls on the same
// Store must yield byte-identical sequences. The reth pipeline relies
// on this for AddLeaf offload — the worker iterates once to compute the
// storage root, the writer iterates again to drive MDBX writes.
func TestStoreIterateRepeatable(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	pairs := [][2][]byte{
		{[]byte("a"), []byte("alpha")},
		{[]byte("b"), []byte("beta")},
		{[]byte("c"), []byte("gamma")},
	}
	for _, p := range pairs {
		if err := s.Put(p[0], p[1]); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	collect := func() [][2][]byte {
		var out [][2][]byte
		if err := s.Iterate(func(k, v []byte) error {
			out = append(out, [2][]byte{append([]byte{}, k...), append([]byte{}, v...)})
			return nil
		}); err != nil {
			t.Fatalf("Iterate: %v", err)
		}
		return out
	}
	first := collect()
	second := collect()
	if len(first) != len(pairs) || len(second) != len(pairs) {
		t.Fatalf("iterate counts: first=%d second=%d want=%d", len(first), len(second), len(pairs))
	}
	for i := range first {
		if !bytes.Equal(first[i][0], second[i][0]) || !bytes.Equal(first[i][1], second[i][1]) {
			t.Errorf("multi-iterate divergence at %d:\n first=(%q,%q)\n second=(%q,%q)",
				i, first[i][0], first[i][1], second[i][0], second[i][1])
		}
	}
}
