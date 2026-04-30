package reth

import (
	"github.com/ethereum/go-ethereum/common"
)

// NodeEmitter is called by HashBuilder once for each branch node that
// completes during streaming. The path is the trie nibble path from root to
// the branch (using StoredNibbles' 33-byte packed form), and node is the
// reth-canonical BranchNodeCompact.
//
// Emissions happen in path-lexicographic order, so the consumer can write to
// AccountsTrie/StoragesTrie via cursor.append (sequential MDBX writes).
type NodeEmitter func(path StoredNibbles, node BranchNodeCompact) error

// HashBuilder is a streaming MPT builder. Consumes keccak-sorted (path, leaf)
// pairs via AddLeaf; emits BranchNodeCompact updates via the NodeEmitter
// callback as branch subtrees complete; returns the final state root from
// Root() after the last AddLeaf.
//
// Algorithm mirrors alloy_trie::HashBuilder: an O(depth) stack of in-progress
// subtrees keyed by the current key prefix. Each AddLeaf extends the rightmost
// stack entry. When a new leaf's prefix diverges from the previous one,
// completed subtrees pop off the stack and emit one BranchNodeCompact per
// branch node.
//
// Memory footprint at any moment: O(trie depth) ≈ a few KB regardless of N.
type HashBuilder struct {
	emit      NodeEmitter
	leafCount int
	// Algorithm state will be added in subsequent tasks. For now the empty-trie
	// case is the only behaviour: Root() with no AddLeaf calls returns
	// EmptyRootHash.
}

// NewHashBuilder returns a HashBuilder that emits completed branch nodes via
// emit. Pass a no-op emit (`func(StoredNibbles, BranchNodeCompact) error
// { return nil }`) for tests that only care about the root hash.
func NewHashBuilder(emit NodeEmitter) *HashBuilder {
	return &HashBuilder{emit: emit}
}

// AddLeaf inserts a (keyNibbles, valueRLP) pair into the trie under
// construction. keyNibbles must be strictly greater than the previous AddLeaf
// call's key (lexicographic on nibble values). valueRLP is the RLP-encoded
// leaf value (typically rlp.Encode(account_struct) or rlp.Encode(storage_value)).
//
// Panics on out-of-order input.
func (b *HashBuilder) AddLeaf(keyNibbles []byte, valueRLP []byte) error {
	b.leafCount++
	// Algorithm body lands in subsequent tasks.
	return nil
}

// Root returns the final MPT root hash. After Root(), the HashBuilder's state
// is undefined — do not call AddLeaf again on the same instance.
//
// The empty-trie case (no AddLeaf calls) returns the canonical empty-MPT hash
// 0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421.
func (b *HashBuilder) Root() common.Hash {
	if b.leafCount == 0 {
		// keccak256(rlp([])) — canonical empty-MPT root
		return common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	}
	// Non-empty case lands in subsequent tasks.
	panic("HashBuilder.Root: non-empty case not yet implemented")
}
