//go:build cgo_reth

package reth

import (
	"bytes"
	"errors"
	"fmt"
	"testing"
)

// TestSorter_PutIterateOrder confirms Pebble's LSM auto-sort: keys inserted
// in reverse order come back ascending.
func TestSorter_PutIterateOrder(t *testing.T) {
	s, err := NewSorter(t.TempDir())
	if err != nil {
		t.Fatalf("NewSorter: %v", err)
	}
	defer s.Close()

	// Insert keys 0x09..0x00 in descending order.
	want := [][]byte{}
	for i := 9; i >= 0; i-- {
		k := []byte{byte(i)}
		v := []byte{byte(i + 100)}
		if err := s.Put(k, v); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
		want = append([][]byte{k}, want...) // build ascending want list
	}

	idx := 0
	err = s.Iterate(func(k, v []byte) error {
		if idx >= len(want) {
			return fmt.Errorf("extra key at idx %d: %x", idx, k)
		}
		if !bytes.Equal(k, want[idx]) {
			return fmt.Errorf("idx %d: got key %x, want %x", idx, k, want[idx])
		}
		idx++
		return nil
	})
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if idx != len(want) {
		t.Fatalf("got %d keys, want %d", idx, len(want))
	}
}

// TestSorter_FlushBoundary inserts >sorterFlushBytes worth of data so the
// batch flushes mid-stream, then verifies all entries are still readable on
// Iterate. Catches any mistake in the flush-then-reset path.
func TestSorter_FlushBoundary(t *testing.T) {
	s, err := NewSorter(t.TempDir())
	if err != nil {
		t.Fatalf("NewSorter: %v", err)
	}
	defer s.Close()

	// Each entry: 8-byte key + 1 KiB value → 70k entries ≈ 70 MiB > 64 MiB.
	const N = 70_000
	const valSize = 1024
	val := make([]byte, valSize)
	for i := 0; i < N; i++ {
		k := make([]byte, 8)
		// Big-endian so iterator order matches numeric order.
		k[0] = byte(i >> 56)
		k[1] = byte(i >> 48)
		k[2] = byte(i >> 40)
		k[3] = byte(i >> 32)
		k[4] = byte(i >> 24)
		k[5] = byte(i >> 16)
		k[6] = byte(i >> 8)
		k[7] = byte(i)
		// Fill value with a per-key marker so we can verify pairing.
		val[0] = byte(i)
		if err := s.Put(k, val); err != nil {
			t.Fatalf("Put %d: %v", i, err)
		}
	}

	count := 0
	err = s.Iterate(func(k, v []byte) error {
		if len(v) != valSize {
			return fmt.Errorf("value size: got %d, want %d", len(v), valSize)
		}
		// Verify ascending order by checking key matches expected counter.
		exp := count
		gotI := int(k[7]) | int(k[6])<<8 | int(k[5])<<16 | int(k[4])<<24 |
			int(k[3])<<32 | int(k[2])<<40 | int(k[1])<<48 | int(k[0])<<56
		if gotI != exp {
			return fmt.Errorf("idx %d: got key int %d", exp, gotI)
		}
		count++
		return nil
	})
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if count != N {
		t.Fatalf("Iterate yielded %d entries, want %d", count, N)
	}
}

// TestSorter_EmptyIterate confirms an unused sorter iterates with zero yields
// and no error.
func TestSorter_EmptyIterate(t *testing.T) {
	s, err := NewSorter(t.TempDir())
	if err != nil {
		t.Fatalf("NewSorter: %v", err)
	}
	defer s.Close()

	yielded := 0
	err = s.Iterate(func(k, v []byte) error {
		yielded++
		return nil
	})
	if err != nil {
		t.Fatalf("Iterate: %v", err)
	}
	if yielded != 0 {
		t.Fatalf("Iterate yielded %d times, want 0", yielded)
	}
}

// TestSorter_CloseIdempotent confirms double Close returns nil both times.
func TestSorter_CloseIdempotent(t *testing.T) {
	s, err := NewSorter(t.TempDir())
	if err != nil {
		t.Fatalf("NewSorter: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("first Close: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("second Close: %v", err)
	}
}

// TestSorter_YieldErrorPropagates confirms a yield-returned sentinel error
// short-circuits Iterate and surfaces unchanged.
func TestSorter_YieldErrorPropagates(t *testing.T) {
	s, err := NewSorter(t.TempDir())
	if err != nil {
		t.Fatalf("NewSorter: %v", err)
	}
	defer s.Close()

	for i := 0; i < 10; i++ {
		if err := s.Put([]byte{byte(i)}, []byte{byte(i)}); err != nil {
			t.Fatalf("Put: %v", err)
		}
	}

	sentinel := errors.New("stop here")
	yielded := 0
	err = s.Iterate(func(k, v []byte) error {
		yielded++
		if yielded == 3 {
			return sentinel
		}
		return nil
	})
	if !errors.Is(err, sentinel) {
		t.Fatalf("got error %v, want sentinel %v", err, sentinel)
	}
	if yielded != 3 {
		t.Fatalf("yielded %d times before stop, want 3", yielded)
	}
}

// TestSorter_PutAfterClose returns a clear error rather than panicking.
func TestSorter_PutAfterClose(t *testing.T) {
	s, err := NewSorter(t.TempDir())
	if err != nil {
		t.Fatalf("NewSorter: %v", err)
	}
	if err := s.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := s.Put([]byte("k"), []byte("v")); err == nil {
		t.Fatalf("Put after Close: expected error, got nil")
	}
}
