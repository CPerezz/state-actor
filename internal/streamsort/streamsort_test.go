package streamsort

import (
	"bytes"
	"crypto/sha256"
	"encoding/binary"
	"errors"
	"fmt"
	"math/rand/v2"
	"strings"
	"sync"
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

	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
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

	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
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
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
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

// TestStoreGetRoundTrip exercises the random-access Get path that the
// disk-backed commitment ctx.Account / ctx.Storage callbacks use.
//
// Asserts:
//   - Get returns the matching value for every Put key, byte-equal
//   - Get on an absent key returns (nil, nil) — NOT an error
//   - Get works both BEFORE any Iterate and AFTER it (no regression in
//     the Pebble-reuse path now that the flush moved to Finalize)
//   - Get returned slices are owned by the caller — modifying them
//     doesn't corrupt Pebble's internal state
func TestStoreGetRoundTrip(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	const N = 100
	puts := make(map[string][]byte, N)
	for i := 0; i < N; i++ {
		k := []byte(fmt.Sprintf("key-%04d", i))
		v := []byte(fmt.Sprintf("value-%04d-padding-for-larger-payload", i))
		puts[string(k)] = v
		if err := s.Put(k, v); err != nil {
			t.Fatalf("Put[%d]: %v", i, err)
		}
	}
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	// Get BEFORE any Iterate.
	for k, want := range puts {
		got, err := s.Get([]byte(k))
		if err != nil {
			t.Fatalf("Get(%q) before Iterate: %v", k, err)
		}
		if !bytes.Equal(got, want) {
			t.Errorf("Get(%q) = %q, want %q", k, got, want)
		}
	}

	// Get on an absent key returns (nil, nil), NOT an error.
	got, err := s.Get([]byte("absent-key"))
	if err != nil {
		t.Errorf("Get(absent): err = %v, want nil", err)
	}
	if got != nil {
		t.Errorf("Get(absent): got = %q, want nil", got)
	}

	// Mutating a returned slice must NOT corrupt subsequent reads.
	const k = "key-0042"
	got, _ = s.Get([]byte(k))
	for i := range got {
		got[i] = 0xff
	}
	again, _ := s.Get([]byte(k))
	if !bytes.Equal(again, puts[k]) {
		t.Errorf("Get(%q) after caller-side mutation: got %q, want %q", k, again, puts[k])
	}

	// Get after Iterate also works (no double-flush regression).
	if err := s.Iterate(func(_, _ []byte) error { return nil }); err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	got, _ = s.Get([]byte("key-0001"))
	if !bytes.Equal(got, puts["key-0001"]) {
		t.Errorf("Get after Iterate: got %q, want %q", got, puts["key-0001"])
	}
}

// --- WRITING → FINALIZED → CLOSED state machine regression tests ---

// TestPutAfterFinalizeErrors: Put-after-Finalize must return an error
// (not a panic) so the state-machine violation surfaces explicitly.
func TestPutAfterFinalizeErrors(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if err := s.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put (pre-Finalize): %v", err)
	}
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}
	err = s.Put([]byte("k2"), []byte("v2"))
	if err == nil {
		t.Fatal("Put after Finalize: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Finalize") {
		t.Errorf("Put error should mention Finalize; got %v", err)
	}
}

// TestGetBeforeFinalizeErrors: Get before Finalize must error so
// callers can't accidentally read a half-flushed batch.
func TestGetBeforeFinalizeErrors(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if err := s.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	_, err = s.Get([]byte("k"))
	if err == nil {
		t.Fatal("Get before Finalize: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Finalize") {
		t.Errorf("Get error should mention Finalize; got %v", err)
	}
}

// TestIterateBeforeFinalizeErrors: same gate for Iterate.
func TestIterateBeforeFinalizeErrors(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if err := s.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	err = s.Iterate(func(_, _ []byte) error { return nil })
	if err == nil {
		t.Fatal("Iterate before Finalize: expected error, got nil")
	}
	if !strings.Contains(err.Error(), "Finalize") {
		t.Errorf("Iterate error should mention Finalize; got %v", err)
	}
}

// TestFinalizeIdempotent: Finalize twice → both calls succeed; the
// underlying batch is committed exactly once (a second commit on a
// committing batch would panic).
func TestFinalizeIdempotent(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	if err := s.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize (first): %v", err)
	}
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize (second): %v", err)
	}
	got, err := s.Get([]byte("k"))
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if !bytes.Equal(got, []byte("v")) {
		t.Errorf("Get after double-Finalize: got %q, want %q", got, "v")
	}
}

// TestCloseBeforeFinalize: the Put-only-never-read lifecycle (e.g. an
// aborted writer goroutine) still cleans up correctly. Close flushes
// the residual batch under putMu without going through Finalize.
func TestCloseBeforeFinalize(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Put([]byte("k"), []byte("v")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
}

// TestConcurrentGetAfterFinalize is the Bug 1 regression bar. It
// reproduces the load shape that crashed the SPEC_TARGET_GB=1 bench:
// N writes, Finalize, then 64 goroutines all calling Get concurrently.
// Without the explicit Finalize + per-reader gating, the pre-Finalize
// streamsort would race on the shared *pebble.Batch.Commit and panic
// with "pebble: batch already committing". With Finalize the read path
// goes straight to pebble.DB.Get, which is thread-safe.
//
// Must run cleanly under `go test -race`.
func TestConcurrentGetAfterFinalize(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	const N = 1_000
	want := make(map[string][]byte, N)
	for i := 0; i < N; i++ {
		k := []byte(fmt.Sprintf("k%05d", i))
		v := []byte(fmt.Sprintf("v%05d", i))
		want[string(k)] = v
		if err := s.Put(k, v); err != nil {
			t.Fatalf("Put[%d]: %v", i, err)
		}
	}
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	const workers = 64
	const opsPer = 200
	var wg sync.WaitGroup
	wg.Add(workers)
	errCh := make(chan error, workers)
	for w := 0; w < workers; w++ {
		go func(seed int) {
			defer wg.Done()
			r := rand.New(rand.NewPCG(uint64(seed)+1, 0xabcdef))
			for op := 0; op < opsPer; op++ {
				idx := int(r.Uint64() % N)
				k := []byte(fmt.Sprintf("k%05d", idx))
				got, err := s.Get(k)
				if err != nil {
					errCh <- fmt.Errorf("worker=%d op=%d Get(%q): %v", seed, op, k, err)
					return
				}
				if !bytes.Equal(got, want[string(k)]) {
					errCh <- fmt.Errorf("worker=%d op=%d Get(%q) = %q, want %q",
						seed, op, k, got, want[string(k)])
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestConcurrentIterateAfterFinalize: 8 goroutines each calling
// Iterate concurrently. Each must see the full sorted set with byte-
// identical values. Pebble's NewIter is documented as safe for
// concurrent callers (iterator.go:177-178); this test guards the
// contract from accidental shared-state regressions in streamsort.
//
// Must run cleanly under `go test -race`.
func TestConcurrentIterateAfterFinalize(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	defer s.Close()

	const N = 500
	want := make(map[string]string, N)
	for i := 0; i < N; i++ {
		k := fmt.Sprintf("k%04d", i)
		v := fmt.Sprintf("v%04d", i)
		want[k] = v
		if err := s.Put([]byte(k), []byte(v)); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	const workers = 8
	var wg sync.WaitGroup
	wg.Add(workers)
	errCh := make(chan error, workers)
	for w := 0; w < workers; w++ {
		go func(id int) {
			defer wg.Done()
			seen := make(map[string]string, N)
			err := s.Iterate(func(k, v []byte) error {
				seen[string(k)] = string(v)
				return nil
			})
			if err != nil {
				errCh <- fmt.Errorf("worker=%d Iterate: %v", id, err)
				return
			}
			if len(seen) != N {
				errCh <- fmt.Errorf("worker=%d saw %d entries, want %d", id, len(seen), N)
				return
			}
			for k, v := range want {
				if seen[k] != v {
					errCh <- fmt.Errorf("worker=%d Iterate[%q] = %q, want %q", id, k, seen[k], v)
					return
				}
			}
		}(w)
	}
	wg.Wait()
	close(errCh)
	for err := range errCh {
		t.Error(err)
	}
}

// TestCloseDrainsReaders validates that Close blocks until in-flight
// readers complete, per the Pebble contract (db.go:1557) that DB.Close
// must not race with any other DB method. Without the readers
// WaitGroup, Close could race with an in-flight Get/Iterate and corrupt
// the Pebble DB shutdown.
//
// Mechanism: start an Iterate that pauses inside yield via a channel;
// kick off Close in a separate goroutine; assert Close has NOT
// returned until we release the iterate. Run under `-race`.
func TestCloseDrainsReaders(t *testing.T) {
	s, err := New(t.TempDir())
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := s.Put([]byte("a"), []byte("1")); err != nil {
		t.Fatalf("Put: %v", err)
	}
	if err := s.Finalize(); err != nil {
		t.Fatalf("Finalize: %v", err)
	}

	yieldEntered := make(chan struct{})
	releaseYield := make(chan struct{})
	iterDone := make(chan error, 1)
	go func() {
		iterDone <- s.Iterate(func(_, _ []byte) error {
			yieldEntered <- struct{}{}
			<-releaseYield
			return nil
		})
	}()

	<-yieldEntered

	closeDone := make(chan error, 1)
	go func() {
		closeDone <- s.Close()
	}()

	// Give Close a chance to (incorrectly) return early — if drain is
	// broken, this races; if drain is correct, Close stays blocked.
	select {
	case err := <-closeDone:
		t.Fatalf("Close returned (err=%v) while Iterate is still running — readers.Wait drain is broken", err)
	default:
		// expected: Close is blocked on readers.Wait
	}

	close(releaseYield)
	if err := <-iterDone; err != nil {
		t.Errorf("Iterate returned err=%v", err)
	}
	if err := <-closeDone; err != nil {
		t.Errorf("Close returned err=%v", err)
	}
}
