package reth

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestStoredNibblesRoundtrip(t *testing.T) {
	cases := [][]byte{
		{},                             // empty path
		{0xa},                          // single nibble
		{0xa, 0xb, 0xc},                // odd
		{0xa, 0xb, 0xc, 0xd},           // even
		{0x0, 0x1, 0x2, 0x3, 0x4, 0x5}, // even, mixed
	}
	// 64-nibble (32-byte) max
	full := make([]byte, 64)
	for i := range full {
		full[i] = byte(i % 16)
	}
	cases = append(cases, full)

	for i, nibbles := range cases {
		sn := StoredNibbles{Length: byte(len(nibbles)), Packed: packNibbles(nibbles)}
		var buf bytes.Buffer
		sn.EncodeKey(&buf)
		if buf.Len() != 33 {
			t.Fatalf("case %d: encoded len=%d, want 33", i, buf.Len())
		}
		var out StoredNibbles
		out.DecodeKey(buf.Bytes())
		if out.Length != sn.Length {
			t.Errorf("case %d: length %d -> %d", i, sn.Length, out.Length)
		}
		if out.Packed != sn.Packed {
			t.Errorf("case %d: packed mismatch hex=%x", i, buf.Bytes())
		}
	}
}

// packNibbles is a test helper — same algorithm as production packing.
func packNibbles(nibbles []byte) common.Hash {
	var packed common.Hash
	for i, n := range nibbles {
		if i%2 == 0 {
			packed[i/2] |= n << 4
		} else {
			packed[i/2] |= n
		}
	}
	return packed
}
