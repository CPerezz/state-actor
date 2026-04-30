package reth

import (
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/trie"
)

// emptyTrieRootHash is the canonical Merkle-Patricia trie root of the empty trie:
// keccak256(rlp([])) = 0x56e81f17...
var emptyTrieRootHash = common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

func TestHashBuilderEmpty(t *testing.T) {
	emit := func(path StoredNibbles, node BranchNodeCompact) error { return nil }
	hb := NewHashBuilder(emit)
	root := hb.Root()
	if root != emptyTrieRootHash {
		t.Errorf("empty trie root = %s, want %s", root.Hex(), emptyTrieRootHash.Hex())
	}
	// Sanity vs. go-ethereum reference:
	if root != trie.NewStackTrie(nil).Hash() {
		t.Errorf("empty trie root != trie.StackTrie's empty root")
	}
}
