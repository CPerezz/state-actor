package besu

import (
	"testing"

	"github.com/ethereum/go-ethereum/crypto"
)

// TestKeccakConstants_DerivedFromSpec recomputes each hash from its canonical
// input bytes (per Besu MerkleTrie.java:34-35, Hash.java:39,51, RLP.java:29).
// A failure here means constants.go is wrong or the keccak primitive changed.
func TestKeccakConstants_DerivedFromSpec(t *testing.T) {
	cases := []struct {
		name string
		in   []byte
		want [32]byte
	}{
		{
			// Hash.java:51 — Hash.EMPTY = keccak256([]).
			"EmptyCodeHash = keccak256([])",
			[]byte{},
			EmptyCodeHash,
		},
		{
			// MerkleTrie.java:34-35 — EMPTY_TRIE_NODE_HASH = keccak256([0x80]).
			// [0x80] is RLP null (RLP.java:29) = the encoding of an empty node.
			"EmptyTrieNodeHash = keccak256([0x80])",
			[]byte{0x80},
			EmptyTrieNodeHash,
		},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := crypto.Keccak256Hash(c.in)
			if got != c.want {
				t.Fatalf("constant mismatch:\n  got:  %s\n  want: %x", got.Hex(), c.want)
			}
		})
	}
}

// TestByteLiterals pins EmptyTrieNode, RlpEmptyList, and LeafTerminator as
// single-byte values. These are referenced by the trie builder (Part 2) and
// must never silently change.
func TestByteLiterals(t *testing.T) {
	if len(EmptyTrieNode) != 1 || EmptyTrieNode[0] != 0x80 {
		t.Fatalf("EmptyTrieNode: got %x, want [0x80]", EmptyTrieNode)
	}
	if len(RlpEmptyList) != 1 || RlpEmptyList[0] != 0xc0 {
		t.Fatalf("RlpEmptyList: got %x, want [0xc0]", RlpEmptyList)
	}
	if LeafTerminator != 0x10 {
		t.Fatalf("LeafTerminator: got %#x, want 0x10", LeafTerminator)
	}
}
