package trie

import (
	"github.com/ethereum/go-ethereum/common"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
)

// Node is the Bonsai trie node interface.
//
// Bonsai reuses standard Ethereum MPT node types. The four concrete
// implementations are NullNode, LeafNode, ExtensionNode, and BranchNode.
//
// Citation: hyperledger/besu tag 26.5.0:
//   - BranchNode.java
//   - LeafNode.java
//   - ExtensionNode.java
//   - NullNode.java
type Node interface {
	// EncodedBytes returns the full RLP encoding of this node.
	EncodedBytes() []byte
	// EncodedBytesRef returns the byte sequence a parent uses to reference
	// this node — hash-RLP if encoded length >= 32, else the encoded bytes
	// inlined.
	EncodedBytesRef() []byte
	// Hash returns keccak256(EncodedBytes()).
	Hash() common.Hash
}

// NullNode is the singleton empty-tree placeholder.
type nullNode struct{}

// NullNodeInstance is the canonical NullNode value. Its RLP is the single
// byte 0x80 (RLP.NULL). MerkleTrie.java:34, RLP.java:29.
var NullNodeInstance Node = nullNode{}

func (nullNode) EncodedBytes() []byte    { return []byte{0x80} }
func (nullNode) EncodedBytesRef() []byte { return []byte{0x80} }
func (nullNode) Hash() common.Hash {
	// keccak256([0x80]) == EMPTY_TRIE_NODE_HASH.
	return common.HexToHash(
		"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
	)
}

// LeafNode stores a nibble path and the leaf value (account RLP for state
// trie; storage value RLP for storage trie).
//
// RLP wire format (LeafNode.java:117-124):
//
//	RLP_LIST [ CompactEncode(path, isLeaf=true), value ]
//
// The path is the *remaining* nibbles after any path-splitting performed by
// the trie builder, and does NOT include the leaf terminator (0x10) — the
// terminator is signaled to CompactEncode via the isLeaf flag.
type leafNode struct {
	path  []byte
	value []byte

	cachedRLP []byte // lazy
}

// NewLeafNode constructs a LeafNode with the given path (nibble-per-byte) and
// value (raw RLP bytes for state-trie accounts, or storage-value RLP).
func NewLeafNode(path, value []byte) Node {
	return &leafNode{path: path, value: value}
}

func (l *leafNode) Path() []byte  { return l.path }
func (l *leafNode) Value() []byte { return l.value }

func (l *leafNode) EncodedBytes() []byte {
	if l.cachedRLP != nil {
		return l.cachedRLP
	}
	hp := CompactEncode(l.path, true)
	rlp, err := gethrlp.EncodeToBytes([][]byte{hp, l.value})
	if err != nil {
		panic("besu/trie: leaf RLP encode: " + err.Error())
	}
	l.cachedRLP = rlp
	return rlp
}

func (l *leafNode) EncodedBytesRef() []byte { return EncodedBytesRef(l.EncodedBytes()) }
func (l *leafNode) Hash() common.Hash       { return NodeHash(l.EncodedBytes()) }

// ExtensionNode stores a shared nibble path and a child node reference.
//
// RLP wire format (ExtensionNode.java:119-127):
//
//	RLP_LIST [ CompactEncode(path, isLeaf=false), child.EncodedBytesRef() ]
type extensionNode struct {
	path  []byte
	child Node

	cachedRLP []byte // lazy
}

// NewExtensionNode constructs an ExtensionNode with the given shared nibble
// path and child node.
func NewExtensionNode(path []byte, child Node) Node {
	if child == nil {
		child = NullNodeInstance
	}
	return &extensionNode{path: path, child: child}
}

func (e *extensionNode) Path() []byte { return e.path }
func (e *extensionNode) Child() Node  { return e.child }

func (e *extensionNode) EncodedBytes() []byte {
	if e.cachedRLP != nil {
		return e.cachedRLP
	}
	hp := CompactEncode(e.path, false)
	// child reference is already an RLP-encoded byte sequence (either
	// inlined node RLP or 0xa0 ++ hash). Wrap with RawValue so geth's RLP
	// encoder treats it as already-encoded bytes.
	rlp, err := gethrlp.EncodeToBytes([]gethrlp.RawValue{
		mustEncodeBytes(hp),
		gethrlp.RawValue(e.child.EncodedBytesRef()),
	})
	if err != nil {
		panic("besu/trie: extension RLP encode: " + err.Error())
	}
	e.cachedRLP = rlp
	return rlp
}

func (e *extensionNode) EncodedBytesRef() []byte { return EncodedBytesRef(e.EncodedBytes()) }
func (e *extensionNode) Hash() common.Hash       { return NodeHash(e.EncodedBytes()) }

// BranchNode is a 17-slot Ethereum MPT branch: 16 children (nibbles 0..15)
// plus a 17th value slot (always nil for state and storage tries — only set
// when the trie has a key that terminates exactly at this node).
//
// RLP wire format (BranchNode.java:130-144):
//
//	RLP_LIST [ child[0].EncodedBytesRef(), ..., child[15].EncodedBytesRef(), 0x80 ]
type branchNode struct {
	children [16]Node
	value    []byte // typically nil

	cachedRLP []byte // lazy
}

// NewBranchNode constructs an empty BranchNode (all 16 children = NullNode,
// value = nil).
func NewBranchNode() *branchNode {
	br := &branchNode{}
	for i := range br.children {
		br.children[i] = NullNodeInstance
	}
	return br
}

// Child returns the node at the given branch slot (0..15).
func (b *branchNode) Child(i int) Node { return b.children[i] }

// SetChild replaces the node at the given branch slot. Resets the cached RLP.
func (b *branchNode) SetChild(i int, n Node) {
	if n == nil {
		n = NullNodeInstance
	}
	b.children[i] = n
	b.cachedRLP = nil
}

func (b *branchNode) EncodedBytes() []byte {
	if b.cachedRLP != nil {
		return b.cachedRLP
	}
	items := make([]gethrlp.RawValue, 17)
	for i, c := range b.children {
		items[i] = gethrlp.RawValue(c.EncodedBytesRef())
	}
	// Slot 17: branch value. State and storage tries always store nil here.
	if b.value == nil {
		items[16] = gethrlp.RawValue([]byte{0x80}) // RLP null
	} else {
		items[16] = mustEncodeBytes(b.value)
	}
	rlp, err := gethrlp.EncodeToBytes(items)
	if err != nil {
		panic("besu/trie: branch RLP encode: " + err.Error())
	}
	b.cachedRLP = rlp
	return rlp
}

func (b *branchNode) EncodedBytesRef() []byte { return EncodedBytesRef(b.EncodedBytes()) }
func (b *branchNode) Hash() common.Hash       { return NodeHash(b.EncodedBytes()) }

// mustEncodeBytes wraps gethrlp.EncodeToBytes for a []byte value.
func mustEncodeBytes(b []byte) gethrlp.RawValue {
	out, err := gethrlp.EncodeToBytes(b)
	if err != nil {
		panic("besu/trie: bytes RLP encode: " + err.Error())
	}
	return gethrlp.RawValue(out)
}
