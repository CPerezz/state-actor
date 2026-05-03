package trie

import (
	"bytes"
	"testing"
)

// TestCompactEncode_Spec mirrors Besu's CompactEncoding.encode.
//
// HP metadata byte layout (CompactEncoding.java:79-112):
//   - 0x00 + (highNibble<<4)  if even-length extension (highNibble forced to 0)
//   - 0x10 + firstNibble       if odd-length extension
//   - 0x20 + (highNibble<<4)  if even-length leaf (highNibble forced to 0)
//   - 0x30 + firstNibble       if odd-length leaf
func TestCompactEncode_Spec(t *testing.T) {
	cases := []struct {
		name    string
		nibbles []byte
		isLeaf  bool
		want    []byte
	}{
		{"empty extension", []byte{}, false, []byte{0x00}},
		{"single nibble extension [0x5]", []byte{0x05}, false, []byte{0x15}},
		{"two nibbles extension [0x1, 0x2]", []byte{0x01, 0x02}, false, []byte{0x00, 0x12}},
		{"three nibbles extension [0x3, 0x4, 0x5]", []byte{0x03, 0x04, 0x05}, false, []byte{0x13, 0x45}},
		{"empty leaf", []byte{}, true, []byte{0x20}},
		{"single nibble leaf [0xa]", []byte{0x0a}, true, []byte{0x3a}},
		{"two nibbles leaf [0xb, 0xc]", []byte{0x0b, 0x0c}, true, []byte{0x20, 0xbc}},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := CompactEncode(c.nibbles, c.isLeaf)
			if !bytes.Equal(got, c.want) {
				t.Fatalf("CompactEncode(%v, leaf=%v):\n  got:  %x\n  want: %x",
					c.nibbles, c.isLeaf, got, c.want)
			}
		})
	}
}

// TestCompactEncode_OutputLength verifies the encoded length is len(nibbles)/2 + 1.
func TestCompactEncode_OutputLength(t *testing.T) {
	for n := 0; n <= 8; n++ {
		nibbles := make([]byte, n)
		for i := range nibbles {
			nibbles[i] = byte(i & 0x0f)
		}
		for _, isLeaf := range []bool{false, true} {
			got := CompactEncode(nibbles, isLeaf)
			want := n/2 + 1
			if len(got) != want {
				t.Fatalf("len(CompactEncode(%d-nibbles, leaf=%v))=%d, want %d",
					n, isLeaf, len(got), want)
			}
		}
	}
}

// TestCompactDecode_RoundTrip exercises decode(encode(nibbles)) == nibbles for
// all combinations 0..8 nibbles and {leaf, extension}.
func TestCompactDecode_RoundTrip(t *testing.T) {
	for n := 0; n <= 8; n++ {
		nibbles := make([]byte, n)
		for i := range nibbles {
			nibbles[i] = byte(i & 0x0f)
		}
		for _, isLeaf := range []bool{false, true} {
			encoded := CompactEncode(nibbles, isLeaf)
			gotNibs, gotLeaf := CompactDecode(encoded)
			if !bytes.Equal(gotNibs, nibbles) {
				t.Fatalf("round-trip nibbles mismatch (n=%d, leaf=%v):\n  got: %x\n  want: %x",
					n, isLeaf, gotNibs, nibbles)
			}
			if gotLeaf != isLeaf {
				t.Fatalf("round-trip leaf flag mismatch (n=%d, leaf=%v): got %v",
					n, isLeaf, gotLeaf)
			}
		}
	}
}

// TestCompactEncode_HighNibbleForcedZero confirms that for even-length paths
// the metadata byte's high nibble (in the "first nibble of path" position) is
// always zero. This guards against accidental nibble-leak into the metadata.
func TestCompactEncode_HighNibbleForcedZero(t *testing.T) {
	// Even-length extension: meta byte = 0x00 (low nibble of meta is forced zero).
	got := CompactEncode([]byte{0x05, 0x06}, false)
	if got[0] != 0x00 {
		t.Fatalf("even-ext meta byte: got %#x, want 0x00", got[0])
	}
	// Even-length leaf: meta byte = 0x20.
	got = CompactEncode([]byte{0x05, 0x06}, true)
	if got[0] != 0x20 {
		t.Fatalf("even-leaf meta byte: got %#x, want 0x20", got[0])
	}
}
