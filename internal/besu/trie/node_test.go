package trie

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
)

// TestNullNode pins NullNode singleton properties.
func TestNullNode(t *testing.T) {
	if !bytes.Equal(NullNodeInstance.EncodedBytes(), []byte{0x80}) {
		t.Fatalf("NullNode.EncodedBytes(): got %x, want [0x80]", NullNodeInstance.EncodedBytes())
	}
	want := common.HexToHash(
		"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
	)
	if NullNodeInstance.Hash() != want {
		t.Fatalf("NullNode.Hash(): got %x, want %x", NullNodeInstance.Hash(), want)
	}
	// NullNode is always inlined (1 byte < 32).
	ref := NullNodeInstance.EncodedBytesRef()
	if !bytes.Equal(ref, []byte{0x80}) {
		t.Fatalf("NullNode.EncodedBytesRef(): got %x, want [0x80]", ref)
	}
}

// TestLeafNode_RLPShape verifies the RLP wire format:
//
//	RLP_LIST [ CompactEncode(path, isLeaf=true), value ]
func TestLeafNode_RLPShape(t *testing.T) {
	// path = [0x01, 0x02], value = [0xab]
	// CompactEncode([0x01, 0x02], true) = [0x20, 0x12]
	// Inner list: [0x20, 0x12], [0xab] → RLP-encoded
	leaf := NewLeafNode([]byte{0x01, 0x02}, []byte{0xab})
	encoded := leaf.EncodedBytes()

	// Decode and verify shape.
	var decoded [][]byte
	if err := gethrlp.DecodeBytes(encoded, &decoded); err != nil {
		t.Fatalf("RLP decode: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("LeafNode RLP: %d items, want 2", len(decoded))
	}
	wantPath := []byte{0x20, 0x12} // even-length leaf
	if !bytes.Equal(decoded[0], wantPath) {
		t.Fatalf("LeafNode HP-encoded path: got %x, want %x", decoded[0], wantPath)
	}
	if !bytes.Equal(decoded[1], []byte{0xab}) {
		t.Fatalf("LeafNode value: got %x, want [0xab]", decoded[1])
	}
}

// TestExtensionNode_RLPShape verifies:
//
//	RLP_LIST [ CompactEncode(path, isLeaf=false), child.EncodedBytesRef() ]
func TestExtensionNode_RLPShape(t *testing.T) {
	// path = [0x05] (odd-length extension), child = NullNode (inlined as 0x80).
	ext := NewExtensionNode([]byte{0x05}, NullNodeInstance)
	encoded := ext.EncodedBytes()

	// Decode using a single-byte RawValue for the second slot since
	// 0x80 inlined is RLP null (zero-length string).
	var decoded []gethrlp.RawValue
	if err := gethrlp.DecodeBytes(encoded, &decoded); err != nil {
		t.Fatalf("RLP decode: %v", err)
	}
	if len(decoded) != 2 {
		t.Fatalf("ExtensionNode RLP: %d items, want 2", len(decoded))
	}
	// Second item must be 0x80 (the NullNode inlined).
	if !bytes.Equal(decoded[1], []byte{0x80}) {
		t.Fatalf("ExtensionNode child ref: got %x, want [0x80]", decoded[1])
	}
}

// TestBranchNode_AllNullChildren verifies a 17-slot RLP list with all 0x80
// children and 0x80 in the value slot.
func TestBranchNode_AllNullChildren(t *testing.T) {
	br := NewBranchNode()
	encoded := br.EncodedBytes()

	var items []gethrlp.RawValue
	if err := gethrlp.DecodeBytes(encoded, &items); err != nil {
		t.Fatalf("RLP decode: %v", err)
	}
	if len(items) != 17 {
		t.Fatalf("BranchNode RLP: got %d items, want 17", len(items))
	}
	for i, it := range items {
		if !bytes.Equal(it, []byte{0x80}) {
			t.Fatalf("BranchNode slot[%d]: got %x, want [0x80]", i, it)
		}
	}
}

// TestBranchNode_WithChild verifies that setting child[3] to a LeafNode
// produces the expected RLP slot 3 (LeafNode RLP since it's < 32 bytes).
func TestBranchNode_WithChild(t *testing.T) {
	leaf := NewLeafNode([]byte{0x01}, []byte{0xab})
	br := NewBranchNode()
	br.SetChild(3, leaf)

	var items []gethrlp.RawValue
	if err := gethrlp.DecodeBytes(br.EncodedBytes(), &items); err != nil {
		t.Fatalf("RLP decode: %v", err)
	}
	// Slot 3 should be the leaf's EncodedBytesRef (inlined since RLP < 32).
	wantRef := leaf.EncodedBytesRef()
	if !bytes.Equal(items[3], wantRef) {
		t.Fatalf("BranchNode slot[3]: got %x, want %x", items[3], wantRef)
	}
	// All other slots remain 0x80.
	for i, it := range items {
		if i == 3 {
			continue
		}
		if !bytes.Equal(it, []byte{0x80}) {
			t.Fatalf("BranchNode slot[%d]: got %x, want [0x80]", i, it)
		}
	}
}

// TestLeafNode_LongValueStored verifies a leaf with large value yields
// EncodedBytesRef = 0xa0 ++ keccak256(rlp).
func TestLeafNode_LongValueStored(t *testing.T) {
	// 100-byte value forces leaf RLP > 32 bytes.
	value := make([]byte, 100)
	for i := range value {
		value[i] = byte(i)
	}
	leaf := NewLeafNode([]byte{0x0a}, value)
	encoded := leaf.EncodedBytes()
	if len(encoded) < 32 {
		t.Fatalf("test premise: expected leaf RLP >= 32, got len=%d", len(encoded))
	}

	ref := leaf.EncodedBytesRef()
	if len(ref) != 33 {
		t.Fatalf("EncodedBytesRef length: got %d, want 33", len(ref))
	}
	if ref[0] != 0xa0 {
		t.Fatalf("EncodedBytesRef prefix: got %#x, want 0xa0", ref[0])
	}
	wantHash := NodeHash(encoded)
	if !bytes.Equal(ref[1:], wantHash[:]) {
		t.Fatalf("EncodedBytesRef hash mismatch:\n  got:  %x\n  want: %x", ref[1:], wantHash)
	}
}

