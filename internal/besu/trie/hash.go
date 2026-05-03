package trie

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// NodeHash returns keccak256(rlp) — the canonical Ethereum MPT node hash.
// Mirrors BranchNode.java:163, LeafNode.java:144, ExtensionNode.java:147
// at hyperledger/besu tag 26.5.0.
func NodeHash(rlp []byte) common.Hash {
	return crypto.Keccak256Hash(rlp)
}

// IsReferencedByHash returns true iff the RLP encoding is at least 32 bytes
// long, in which case the parent stores a hash reference rather than the
// inlined node. Source: Node.java:52-53 (getEncodedBytes().size() >= 32).
func IsReferencedByHash(rlp []byte) bool {
	return len(rlp) >= 32
}

// EncodedBytesRef returns the byte sequence a parent node uses to reference
// this node:
//
//   - if len(rlp) >= 32: 0xa0 ++ keccak256(rlp) (33 bytes; RLP-encoded 32-byte hash).
//   - if len(rlp) <  32: the rlp itself, inlined verbatim.
//
// Source: Node.getEncodedBytesRef() via BranchNode.java:147-153.
//
// 0xa0 is the RLP prefix for a 32-byte string (0x80 + 32 = 0xa0).
func EncodedBytesRef(rlp []byte) []byte {
	if !IsReferencedByHash(rlp) {
		// Return a fresh copy so callers can safely retain it.
		out := make([]byte, len(rlp))
		copy(out, rlp)
		return out
	}
	hash := NodeHash(rlp)
	out := make([]byte, 33)
	out[0] = 0xa0
	copy(out[1:], hash[:])
	return out
}
