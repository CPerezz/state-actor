package trie

import (
	"bytes"
	"testing"
)

// TestEncodeHexPrefix_NibbleParityMatrix exhaustively covers every
// even/odd × leaf/extension × small-path combination Nethermind's
// HexPrefix.cs:CopyToSpan produces. Each row was hand-derived from the
// algorithm and re-derived by tracing the C# loop; if a row breaks, the
// encoder diverged from Nethermind.
func TestEncodeHexPrefix_NibbleParityMatrix(t *testing.T) {
	cases := []struct {
		name   string
		path   []byte
		isLeaf bool
		want   []byte
	}{
		// --- Empty path (length 0, even) ---
		{"empty_extension", []byte{}, false, []byte{0x00}},
		{"empty_leaf", []byte{}, true, []byte{0x20}},

		// --- Single nibble (length 1, odd) ---
		{"one_nibble_a_extension", []byte{0xa}, false, []byte{0x1a}},
		{"one_nibble_a_leaf", []byte{0xa}, true, []byte{0x3a}},
		{"one_nibble_zero_extension", []byte{0x0}, false, []byte{0x10}},
		{"one_nibble_f_leaf", []byte{0xf}, true, []byte{0x3f}},

		// --- Two nibbles (length 2, even) ---
		{"two_nibbles_extension", []byte{0x1, 0x2}, false, []byte{0x00, 0x12}},
		{"two_nibbles_leaf", []byte{0x1, 0x2}, true, []byte{0x20, 0x12}},
		{"two_nibbles_zeros_extension", []byte{0x0, 0x0}, false, []byte{0x00, 0x00}},
		{"two_nibbles_max_leaf", []byte{0xf, 0xf}, true, []byte{0x20, 0xff}},

		// --- Three nibbles (length 3, odd) ---
		{"three_nibbles_extension", []byte{0xa, 0xb, 0xc}, false, []byte{0x1a, 0xbc}},
		{"three_nibbles_leaf", []byte{0xa, 0xb, 0xc}, true, []byte{0x3a, 0xbc}},

		// --- Four nibbles (length 4, even) ---
		{"four_nibbles_leaf", []byte{0x1, 0x2, 0x3, 0x4}, true, []byte{0x20, 0x12, 0x34}},
		{"four_nibbles_extension", []byte{0x1, 0x2, 0x3, 0x4}, false, []byte{0x00, 0x12, 0x34}},

		// --- Five nibbles (length 5, odd) ---
		{"five_nibbles_leaf", []byte{0xa, 0xb, 0xc, 0xd, 0xe}, true, []byte{0x3a, 0xbc, 0xde}},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := EncodeHexPrefix(c.path, c.isLeaf)
			if !bytes.Equal(got, c.want) {
				t.Fatalf("EncodeHexPrefix(%v, %t):\n  got:  %x\n  want: %x", c.path, c.isLeaf, got, c.want)
			}
		})
	}
}

// TestEncodeHexPrefix_FullKey64Nibbles covers the production-size case for
// state and storage trie leaves: paths are exactly 64 nibbles (32 bytes
// after HP encoding + 1 prefix byte = 33 total).
func TestEncodeHexPrefix_FullKey64Nibbles(t *testing.T) {
	// 64 nibbles spanning [0, 15] cyclically.
	path := make([]byte, 64)
	for i := range path {
		path[i] = byte(i % 16)
	}

	got := EncodeHexPrefix(path, true)

	if len(got) != 33 {
		t.Fatalf("output length: got %d, want 33 (32 paired bytes + 1 HP prefix)", len(got))
	}
	if got[0] != 0x20 {
		t.Errorf("HP prefix: got 0x%02x, want 0x20 (even leaf)", got[0])
	}
	// Pair byte i: nibble[2i]<<4 | nibble[2i+1].
	for i := 0; i < 32; i++ {
		expected := byte(16*(i*2%16) + (i*2+1)%16)
		if got[i+1] != expected {
			t.Errorf("byte[%d]: got 0x%02x, want 0x%02x", i+1, got[i+1], expected)
		}
	}
}

// TestEncodeHexPrefix_OutputLengthFormula verifies the output is always
// `len(path)/2 + 1` bytes, regardless of leaf flag — Nethermind's
// `HexPrefix.ByteLength` formula.
func TestEncodeHexPrefix_OutputLengthFormula(t *testing.T) {
	for n := 0; n <= 70; n++ {
		path := make([]byte, n)
		want := n/2 + 1
		if got := len(EncodeHexPrefix(path, false)); got != want {
			t.Errorf("len(EncodeHexPrefix(extension, n=%d)): got %d, want %d", n, got, want)
		}
		if got := len(EncodeHexPrefix(path, true)); got != want {
			t.Errorf("len(EncodeHexPrefix(leaf, n=%d)): got %d, want %d", n, got, want)
		}
	}
}

// TestEncodeHexPrefix_LeafBitInvariant: the high nibble of the first byte
// always contains both the leaf flag (bit 5) and the parity flag (bit 4).
// Bits 6 and 7 must always be 0 (Nethermind asserts this implicitly).
func TestEncodeHexPrefix_LeafBitInvariant(t *testing.T) {
	for n := 0; n <= 8; n++ {
		path := make([]byte, n)
		for i := range path {
			path[i] = byte(i % 16)
		}
		for _, isLeaf := range []bool{false, true} {
			got := EncodeHexPrefix(path, isLeaf)
			top := got[0] & 0xc0
			if top != 0 {
				t.Errorf("n=%d isLeaf=%t: top 2 bits non-zero (0x%02x)", n, isLeaf, got[0])
			}
		}
	}
}
