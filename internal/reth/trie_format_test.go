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

func TestBranchNodeCompactRoundtrip(t *testing.T) {
	h1 := common.HexToHash("0x1111111111111111111111111111111111111111111111111111111111111111")
	h2 := common.HexToHash("0x2222222222222222222222222222222222222222222222222222222222222222")
	cases := []BranchNodeCompact{
		// minimal: no children
		{StateMask: 0, TreeMask: 0, HashMask: 0, Hashes: nil, RootHash: nil},
		// one hashed child
		{StateMask: 0x0001, TreeMask: 0, HashMask: 0x0001, Hashes: []common.Hash{h1}, RootHash: nil},
		// two hashed children + root
		{StateMask: 0x0003, TreeMask: 0x0002, HashMask: 0x0003, Hashes: []common.Hash{h1, h2}, RootHash: &h1},
		// full state, all hashed
		{
			StateMask: 0xffff, TreeMask: 0x0000, HashMask: 0xffff,
			Hashes:   []common.Hash{h1, h2, h1, h2, h1, h2, h1, h2, h1, h2, h1, h2, h1, h2, h1, h2},
			RootHash: &h2,
		},
	}
	for i, in := range cases {
		var buf bytes.Buffer
		n := in.EncodeCompact(&buf)
		var out BranchNodeCompact
		consumed := out.DecodeCompact(buf.Bytes(), n)
		if consumed != n {
			t.Errorf("case %d: consumed %d, encoded %d", i, consumed, n)
		}
		if !branchNodeEqual(in, out) {
			t.Errorf("case %d: in=%+v out=%+v hex=%x", i, in, out, buf.Bytes())
		}
	}
}

func branchNodeEqual(a, b BranchNodeCompact) bool {
	if a.StateMask != b.StateMask || a.TreeMask != b.TreeMask || a.HashMask != b.HashMask {
		return false
	}
	if len(a.Hashes) != len(b.Hashes) {
		return false
	}
	for i := range a.Hashes {
		if a.Hashes[i] != b.Hashes[i] {
			return false
		}
	}
	if (a.RootHash == nil) != (b.RootHash == nil) {
		return false
	}
	if a.RootHash != nil && *a.RootHash != *b.RootHash {
		return false
	}
	return true
}
