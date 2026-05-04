package trie

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestNodeHash_EmptyTrieNode verifies keccak256([0x80]) matches Besu's
// EMPTY_TRIE_NODE_HASH constant (MerkleTrie.java:35).
func TestNodeHash_EmptyTrieNode(t *testing.T) {
	got := NodeHash([]byte{0x80})
	want := common.HexToHash(
		"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
	)
	if got != want {
		t.Fatalf("NodeHash([0x80]) mismatch:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestIsReferencedByHash pins the inline-vs-stored boundary at exactly 32 bytes.
// Source: Node.java:52-53 — getEncodedBytes().size() >= 32.
func TestIsReferencedByHash(t *testing.T) {
	cases := []struct {
		size int
		want bool
	}{
		{0, false},
		{1, false},
		{31, false},
		{32, true},
		{33, true},
		{1000, true},
	}
	for _, c := range cases {
		got := IsReferencedByHash(make([]byte, c.size))
		if got != c.want {
			t.Fatalf("IsReferencedByHash(size=%d): got %v, want %v", c.size, got, c.want)
		}
	}
}

// TestEncodedBytesRef_Inline verifies that nodes < 32 bytes return raw RLP.
func TestEncodedBytesRef_Inline(t *testing.T) {
	rlp := []byte{0xc0, 0x01, 0x02, 0x03} // 4-byte RLP, well under 32
	got := EncodedBytesRef(rlp)
	if !bytes.Equal(got, rlp) {
		t.Fatalf("inline EncodedBytesRef:\n  got:  %x\n  want: %x", got, rlp)
	}
}

// TestEncodedBytesRef_Stored verifies that nodes >= 32 bytes return
// 0xa0 ++ keccak256(rlp) (33 bytes total).
func TestEncodedBytesRef_Stored(t *testing.T) {
	rlp := make([]byte, 32)
	for i := range rlp {
		rlp[i] = byte(i)
	}
	got := EncodedBytesRef(rlp)
	if len(got) != 33 {
		t.Fatalf("stored EncodedBytesRef length: got %d, want 33", len(got))
	}
	if got[0] != 0xa0 {
		t.Fatalf("stored EncodedBytesRef prefix: got %#x, want 0xa0", got[0])
	}
	want := NodeHash(rlp)
	if !bytes.Equal(got[1:], want[:]) {
		t.Fatalf("stored EncodedBytesRef hash mismatch:\n  got:  %x\n  want: %x",
			got[1:], want)
	}
}

