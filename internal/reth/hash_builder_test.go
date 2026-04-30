package reth

import (
	"bytes"
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

func TestHashBuilderSingleLeaf(t *testing.T) {
	emit := func(path StoredNibbles, node BranchNodeCompact) error {
		t.Errorf("unexpected emission for single leaf: path=%+v node=%+v", path, node)
		return nil
	}
	hb := NewHashBuilder(emit)
	key := bytes.Repeat([]byte{0xa0}, 32)
	value := bytes.Repeat([]byte{0x42}, 32)
	// AddLeaf takes nibble-unpacked keys (64 nibbles for 32-byte key)
	nibbles := bytesToNibbles(key)
	if err := hb.AddLeaf(nibbles, value); err != nil {
		t.Fatalf("AddLeaf: %v", err)
	}
	got := hb.Root()
	st := trie.NewStackTrie(nil)
	st.Update(key, value)
	want := st.Hash()
	if got != want {
		t.Errorf("single-leaf root: got=%s want=%s", got.Hex(), want.Hex())
	}
}

func TestHashBuilderTwoLeaves_Shared(t *testing.T) {
	hb := NewHashBuilder(func(StoredNibbles, BranchNodeCompact) error { return nil })
	k1 := bytes.Repeat([]byte{0xa0}, 32)
	k2 := bytes.Repeat([]byte{0xa0}, 32)
	k2[31] = 0xa1
	if err := hb.AddLeaf(bytesToNibbles(k1), []byte{0x01}); err != nil {
		t.Fatal(err)
	}
	if err := hb.AddLeaf(bytesToNibbles(k2), []byte{0x02}); err != nil {
		t.Fatal(err)
	}
	got := hb.Root()
	st := trie.NewStackTrie(nil)
	st.Update(k1, []byte{0x01})
	st.Update(k2, []byte{0x02})
	want := st.Hash()
	if got != want {
		t.Errorf("shared-prefix root: got=%s want=%s", got.Hex(), want.Hex())
	}
}

func TestHashBuilderTwoLeaves_Diverged(t *testing.T) {
	hb := NewHashBuilder(func(StoredNibbles, BranchNodeCompact) error { return nil })
	k1 := make([]byte, 32)
	k1[0] = 0x10
	k2 := make([]byte, 32)
	k2[0] = 0x20
	if err := hb.AddLeaf(bytesToNibbles(k1), []byte{0x01}); err != nil {
		t.Fatal(err)
	}
	if err := hb.AddLeaf(bytesToNibbles(k2), []byte{0x02}); err != nil {
		t.Fatal(err)
	}
	got := hb.Root()
	st := trie.NewStackTrie(nil)
	st.Update(k1, []byte{0x01})
	st.Update(k2, []byte{0x02})
	want := st.Hash()
	if got != want {
		t.Errorf("diverged root: got=%s want=%s", got.Hex(), want.Hex())
	}
}

func TestHashBuilderThreeTopDiverged(t *testing.T) {
	hb := NewHashBuilder(func(StoredNibbles, BranchNodeCompact) error { return nil })
	mk := func(b byte) []byte { k := make([]byte, 32); k[0] = b; return k }
	keys := [][]byte{mk(0x10), mk(0x20), mk(0x30)}
	values := [][]byte{{0x01}, {0x02}, {0x03}}
	for i := range keys {
		if err := hb.AddLeaf(bytesToNibbles(keys[i]), values[i]); err != nil {
			t.Fatal(err)
		}
	}
	got := hb.Root()
	st := trie.NewStackTrie(nil)
	for i := range keys {
		st.Update(keys[i], values[i])
	}
	want := st.Hash()
	if got != want {
		t.Errorf("three-top-diverged root: got=%s want=%s", got.Hex(), want.Hex())
	}
}

func TestHashBuilderFullBranch16(t *testing.T) {
	hb := NewHashBuilder(func(StoredNibbles, BranchNodeCompact) error { return nil })
	mk := func(b byte) []byte { k := make([]byte, 32); k[0] = b; return k }
	st := trie.NewStackTrie(nil)
	for i := byte(0); i < 16; i++ {
		k := mk(i << 4)
		v := []byte{i + 1}
		if err := hb.AddLeaf(bytesToNibbles(k), v); err != nil {
			t.Fatal(err)
		}
		st.Update(k, v)
	}
	got := hb.Root()
	want := st.Hash()
	if got != want {
		t.Errorf("full-branch-16 root: got=%s want=%s", got.Hex(), want.Hex())
	}
}
