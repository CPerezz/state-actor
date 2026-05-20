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
		sn := StoredNibbles{Length: byte(len(nibbles))}
		copy(sn.Nibbles[:], nibbles)
		var buf bytes.Buffer
		sn.EncodeKey(&buf)
		if buf.Len() != 65 {
			t.Fatalf("case %d: encoded len=%d, want 65", i, buf.Len())
		}
		var out StoredNibbles
		out.DecodeKey(buf.Bytes())
		if out.Length != sn.Length {
			t.Errorf("case %d: length %d -> %d", i, sn.Length, out.Length)
		}
		if out.Nibbles != sn.Nibbles {
			t.Errorf("case %d: nibbles mismatch hex=%x", i, buf.Bytes())
		}
	}
}

// TestStoredNibblesEncodeAccountKey_VariableLength asserts the AccountsTrie
// key wire format: raw nibbles (one byte per nibble), no padding, no length
// suffix. Matches reth's StoredNibbles::Encode = ArrayVec<u8, 64>
// (reth/crates/storage/db-api/src/models/mod.rs:121-127).
func TestStoredNibblesEncodeAccountKey_VariableLength(t *testing.T) {
	cases := [][]byte{
		{},                                                     // empty path = depth 0 (root)
		{0xa},                                                  // 1 nibble
		{0xa, 0xb, 0xc},                                        // 3 nibbles
		bytes.Repeat([]byte{0x0f}, 32),                         // 32 nibbles
		func() []byte { b := make([]byte, 64); for i := range b { b[i] = byte(i % 16) }; return b }(), // full 64-nibble leaf
	}
	for i, nibbles := range cases {
		sn := StoredNibbles{Length: byte(len(nibbles))}
		copy(sn.Nibbles[:], nibbles)
		var buf bytes.Buffer
		sn.EncodeAccountKey(&buf)
		if buf.Len() != len(nibbles) {
			t.Errorf("case %d: encoded len=%d, want %d (no padding, no length byte)", i, buf.Len(), len(nibbles))
		}
		if !bytes.Equal(buf.Bytes(), nibbles) {
			t.Errorf("case %d: encoded=%x want=%x", i, buf.Bytes(), nibbles)
		}

		var out StoredNibbles
		out.DecodeAccountKey(buf.Bytes())
		if out.Length != sn.Length {
			t.Errorf("case %d: length %d -> %d", i, sn.Length, out.Length)
		}
		for j := 0; j < int(sn.Length); j++ {
			if out.Nibbles[j] != sn.Nibbles[j] {
				t.Errorf("case %d: nibble[%d] %d -> %d", i, j, sn.Nibbles[j], out.Nibbles[j])
			}
		}
	}
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

func TestStorageTrieEntryRoundtrip(t *testing.T) {
	h := common.HexToHash("0xabc")
	subKey := StoredNibbles{Length: 4}
	copy(subKey.Nibbles[:], []byte{1, 2, 3, 4})
	in := StorageTrieEntry{
		SubKey: subKey,
		Node: BranchNodeCompact{
			StateMask: 0x0001, TreeMask: 0, HashMask: 0x0001,
			Hashes: []common.Hash{h}, RootHash: nil,
		},
	}
	var buf bytes.Buffer
	n := in.EncodeCompact(&buf)
	var out StorageTrieEntry
	consumed := out.DecodeCompact(buf.Bytes(), n)
	if consumed != n {
		t.Errorf("consumed %d, encoded %d", consumed, n)
	}
	if in.SubKey != out.SubKey {
		t.Errorf("SubKey roundtrip mismatch")
	}
	if !branchNodeEqual(in.Node, out.Node) {
		t.Errorf("Node roundtrip mismatch hex=%x", buf.Bytes())
	}
}
