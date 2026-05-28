package eliasfano

import (
	"bytes"
	"errors"
	"testing"
)

// TestNewRejectsZeroCount pins the count==0 guard. Erigon panics; we return
// an error.
func TestNewRejectsZeroCount(t *testing.T) {
	_, err := New(0, 100)
	if err == nil {
		t.Fatal("New(0, _) returned nil error; expected count-must-be-positive")
	}
}

// TestAddOffsetMonotonicity rejects out-of-order writes.
func TestAddOffsetMonotonicity(t *testing.T) {
	b, err := New(3, 100)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := b.AddOffset(5); err != nil {
		t.Fatalf("first AddOffset(5): %v", err)
	}
	if err := b.AddOffset(4); !errors.Is(err, ErrNonMonotonic) {
		t.Fatalf("AddOffset(4) after AddOffset(5): want ErrNonMonotonic, got %v", err)
	}
}

// TestAddOffsetTooMany rejects writes past count.
func TestAddOffsetTooMany(t *testing.T) {
	b, err := New(2, 100)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 2; i++ {
		if err := b.AddOffset(uint64(i) * 10); err != nil {
			t.Fatalf("AddOffset %d: %v", i, err)
		}
	}
	if err := b.AddOffset(99); !errors.Is(err, ErrTooMany) {
		t.Fatalf("AddOffset past count: want ErrTooMany, got %v", err)
	}
}

// TestAddOffsetOutOfRange rejects offset > maxOffset.
func TestAddOffsetOutOfRange(t *testing.T) {
	b, err := New(2, 100)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	if err := b.AddOffset(101); !errors.Is(err, ErrOutOfRange) {
		t.Fatalf("AddOffset(101) maxOffset=100: want ErrOutOfRange, got %v", err)
	}
}

// TestBuildIdempotent — Build can be called repeatedly; second call is no-op.
func TestBuildIdempotent(t *testing.T) {
	b, err := New(3, 100)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := 0; i < 3; i++ {
		if err := b.AddOffset(uint64(i) * 10); err != nil {
			t.Fatalf("AddOffset: %v", err)
		}
	}
	b.Build()
	b.Build() // must not panic / corrupt state
	if !b.built {
		t.Fatal("Build() did not mark builder as built")
	}
}

// TestAppendBytesShape verifies the wire format header (16 BE bytes for
// count + u) and that the data section is a multiple of 8 bytes.
func TestAppendBytesShape(t *testing.T) {
	b, err := New(5, 256)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for _, off := range []uint64{0, 10, 20, 30, 40} {
		if err := b.AddOffset(off); err != nil {
			t.Fatalf("AddOffset(%d): %v", off, err)
		}
	}
	b.Build()
	buf := b.AppendBytes(nil)
	if len(buf) < 16 {
		t.Fatalf("AppendBytes shorter than 16-byte header: %d", len(buf))
	}
	if (len(buf)-16)%8 != 0 {
		t.Errorf("data tail not word-aligned: %d bytes after 16-byte header", len(buf)-16)
	}
	// count: Builder stores count-1 internally (Erigon convention). For
	// New(5, _), b.count == 4, and AppendBytes emits b.count BE.
	wantCount := []byte{0, 0, 0, 0, 0, 0, 0, 4}
	if !bytes.Equal(buf[:8], wantCount) {
		t.Errorf("count header BE: got %v, want %v", buf[:8], wantCount)
	}
	// u: maxOffset + 1 = 257.
	wantU := []byte{0, 0, 0, 0, 0, 0, 0x01, 0x01}
	if !bytes.Equal(buf[8:16], wantU) {
		t.Errorf("u header BE: got %v, want %v", buf[8:16], wantU)
	}
}

// TestRoundTripAppendBytesSerialize asserts both emission paths produce
// identical bytes.
func TestRoundTripAppendBytesSerialize(t *testing.T) {
	b, err := New(10, 1000)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	for i := uint64(0); i < 10; i++ {
		if err := b.AddOffset(i * 87); err != nil {
			t.Fatalf("AddOffset: %v", err)
		}
	}
	b.Build()
	viaAppend := b.AppendBytes(nil)
	var viaSerialize bytes.Buffer
	if err := b.Serialize(&viaSerialize); err != nil {
		t.Fatalf("Serialize: %v", err)
	}
	if !bytes.Equal(viaAppend, viaSerialize.Bytes()) {
		t.Fatal("AppendBytes vs Serialize: byte-output mismatch")
	}
}

// TestComputeLayout pins the layout helper math against hand-computed values.
// The values here are derived from Erigon's formula at elias_fano.go:187-195
// applied to small inputs.
func TestComputeLayout(t *testing.T) {
	type want struct{ l uint64; wLow, wUp, total int }
	cases := []struct {
		count, maxOff uint64
		w             want
	}{
		// count=1, maxOff=0: l=0, wLow=1 (pure padding), wUp=1.
		// jumpSizeWords: count+1=1, /superQ=0 (no whole blocks),
		// %superQ=1 (partial), partial = 1 + ((1+q-1)/q + 3)/2 =
		// 1 + (1+3)/2 = 3 → total = 1+1+3 = 5.
		{1, 0, want{l: 0, wLow: 1, wUp: 1, total: 5}},
	}
	for _, tc := range cases {
		b, err := New(tc.count, tc.maxOff)
		if err != nil {
			t.Fatalf("New(%d, %d): %v", tc.count, tc.maxOff, err)
		}
		gotL, gotLow, gotUp, gotTotal := b.computeLayout()
		if gotL != tc.w.l || gotLow != tc.w.wLow || gotUp != tc.w.wUp || gotTotal != tc.w.total {
			t.Errorf("computeLayout(%d, %d): got (l=%d wLow=%d wUp=%d total=%d), want (l=%d wLow=%d wUp=%d total=%d)",
				tc.count, tc.maxOff,
				gotL, gotLow, gotUp, gotTotal,
				tc.w.l, tc.w.wLow, tc.w.wUp, tc.w.total)
		}
	}
}
