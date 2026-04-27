package trie

import (
	gethrlp "github.com/ethereum/go-ethereum/rlp"
)

// ChildRef represents a child slot in a branch or extension node.
//
// MPT child references are one of three forms:
//
//   - **Hash reference**: child's RLP is ≥ 32 bytes, parent stores the 32-byte
//     keccak — encoded as a 33-byte RLP byte string (`0xa0 || hash`).
//   - **Inline RLP**: child's RLP is < 32 bytes, parent embeds the child's
//     full RLP verbatim (no extra length prefix).
//   - **Null** (branch only): no child at this slot, encoded as the empty
//     byte string `0x80`.
//
// Exactly one of Keccak / InlineRLP must be set for a real child; leave both
// zero-valued for a null branch slot. The decision between hash-vs-inline is
// up to the caller (typically the trie builder, which knows each child's
// RLP length).
type ChildRef struct {
	Keccak    *[32]byte
	InlineRLP []byte
}

// rawRLP returns the byte sequence this child contributes inside the
// parent node's RLP list. It is already RLP-shaped for direct insertion.
func (c ChildRef) rawRLP() []byte {
	if c.Keccak != nil {
		// 32-byte keccak → RLP byte string: 0xa0 prefix + 32 bytes.
		out := make([]byte, 33)
		out[0] = 0xa0
		copy(out[1:], c.Keccak[:])
		return out
	}
	if len(c.InlineRLP) > 0 {
		// Inline RLP: caller has pre-encoded the child; copy verbatim.
		return c.InlineRLP
	}
	// Null child (branch slot only): the empty byte string.
	return []byte{0x80}
}

// EncodeLeaf returns the RLP encoding of a leaf node `[HP(path, true), value]`.
//
// `path` is the leaf's nibble path (each byte ∈ [0, 15]). `value` is the
// raw bytes stored at the leaf (e.g., the account RLP for a state-trie
// leaf, or the storage-slot value bytes for a storage-trie leaf) — the
// encoder wraps it as an RLP byte string per Nethermind's `EncodeLeaf` at
// `Nethermind.Trie/TrieNode.Decoder.cs:EncodeLeaf`.
func EncodeLeaf(path []byte, value []byte) []byte {
	hp := EncodeHexPrefix(path, true)
	out, err := gethrlp.EncodeToBytes([]any{hp, value})
	if err != nil {
		// EncodeToBytes only errors on encoder-side bugs (e.g. unsupported
		// types). Both inputs here are []byte; a panic is appropriate.
		panic(err)
	}
	return out
}

// EncodeExtension returns the RLP encoding of an extension node
// `[HP(path, false), childRef]`.
//
// Mirrors `Nethermind.Trie/TrieNode.Decoder.cs:EncodeExtension`. The child
// is either a 32-byte hash (encoded as an RLP byte string) or inline RLP
// (copied verbatim) — the decision is encoded in the ChildRef.
func EncodeExtension(path []byte, child ChildRef) []byte {
	hp := EncodeHexPrefix(path, false)
	out, err := gethrlp.EncodeToBytes([]any{hp, gethrlp.RawValue(child.rawRLP())})
	if err != nil {
		panic(err)
	}
	return out
}

// EncodeBranch returns the RLP encoding of a 17-element branch node:
// 16 children + 1 value.
//
// For state and storage tries the value slot is always empty (`0x80`)
// because all values live at leaves — the leaf-path-length 64 means
// branch nodes never carry values themselves. Pass `nil` (or an empty
// byte slice) for `value` to emit the empty byte string.
//
// Mirrors `Nethermind.Trie/TrieNode.Decoder.cs:RlpEncodeBranch`. Nethermind
// always writes `resultSpan[position] = 128` (0x80) at slot 16; we keep
// the parameter for completeness with consumers that emit non-empty
// branch values (rare in modern Ethereum).
func EncodeBranch(children [16]ChildRef, value []byte) []byte {
	items := make([]any, 17)
	for i, c := range children {
		items[i] = gethrlp.RawValue(c.rawRLP())
	}
	items[16] = value // []byte → RLP byte string (empty → 0x80)
	out, err := gethrlp.EncodeToBytes(items)
	if err != nil {
		panic(err)
	}
	return out
}
