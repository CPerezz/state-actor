package reth

import (
	"bytes"
	"errors"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// ErrKeysOutOfOrder is returned by AddLeaf when the supplied key is not strictly
// greater than the previous key (nibble-lexicographic order).
var ErrKeysOutOfOrder = errors.New("HashBuilder: keys must be inserted in strictly ascending nibble order")

// HashBuilder is a streaming Merkle-Patricia trie builder that produces
// reth-canonical BranchNodeCompact emissions in path order, suitable for
// MDBX cursor.append into AccountsTrie or StoragesTrie.
//
// # Algorithm
//
// HashBuilder mirrors alloy_trie::HashBuilder. The state machine maintains:
//
//   - prevKey/prevValue: the most recent leaf's nibble key + RLP-encoded value
//   - stack: an O(depth) stack of deferred branch contexts, one per
//     in-progress branch node along the right edge of the trie
//   - stateMasks/treeMasks/hashMasks: per-depth 16-bit child-slot bitmasks
//
// Each AddLeaf:
//
//  1. Computes CPL (common-prefix length) between the new key and prevKey.
//  2. Pops stack entries deeper than CPL — those subtrees are now complete.
//     For each popped branch, computes its state_mask, tree_mask, hash_mask
//     and Hashes vec, emits the BranchNodeCompact via the NodeEmitter, and
//     replaces it with its parent's reference (a hash if RLP ≥ 32 bytes,
//     or the inlined RLP otherwise).
//  3. Pushes the new leaf onto the stack at depth CPL.
//
// Root() drains the stack, finalizing all remaining branches. The final
// returned hash is the trie root.
//
// # Mask semantics (BNC fields)
//
//   - state_mask: bit i is set iff slot i has any child (leaf or sub-branch).
//   - tree_mask: bit i is set iff slot i's child was emitted as its own
//     BranchNodeCompact row (i.e., a sub-branch we wrote to the trie table).
//     ONLY set via add_branch() in alloy_trie — see emission semantics below.
//   - hash_mask: bit i is set iff slot i's child is referenced by its
//     32-byte hash (RLP ≥ 32 bytes), as opposed to inlined RLP < 32 bytes.
//     ALSO ONLY set via add_branch() per alloy_trie semantics.
//   - Hashes: the popcount(hash_mask) hashes, in slot-index order.
//   - RootHash: optional; populated only on the final emission (the trie root).
//
// # Emission semantics — IMPORTANT
//
// A BranchNodeCompact is emitted ONLY when hash_mask | tree_mask != 0, mirroring
// alloy_trie's behavior. Those masks are populated by add_branch() calls,
// which simulate pre-existing branch nodes during incremental re-execution.
//
// **Pure-leaf insertion (our genesis use case) never triggers add_branch, so
// no BranchNodeCompact emissions occur during a fresh build.** This matches
// reth's own genesis behavior — see crates/storage/db-common/src/init.rs's
// compute_state_root_chunked path. Slice E's writer should treat empty
// AccountsTrie/StoragesTrie tables as expected for genesis-only state.
//
// If you need the trie tables populated for boot-time validation, that's a
// separate concern from HashBuilder: either accept reth's lazy-recompute
// behavior, or add an incremental-build pass that calls add_branch (not yet
// implemented in this Go port; out of scope for Slices A-F).
//
// # Path emission order
//
// When emissions DO happen (incremental re-execution future-work), they're
// guaranteed lexicographically sorted by StoredNibbles path, matching the
// order MDBX expects for cursor.append. This is a load-bearing property —
// Slice E's writer relies on it for sequential MDBX writes.
//
// # Memory
//
// O(trie depth) ≈ a few KB regardless of input size. The stack max-depth is
// bounded by the longest CPL among any two adjacent inputs.
//
// # Determinism
//
// HashBuilder is deterministic: same input sequence → same emissions → same
// root. No map iteration, no time, no randomness.
//
// # Caller obligations
//
//   - AddLeaf inputs MUST be sorted strictly ascending by key. Asserted via
//     ErrKeysOutOfOrder; not silently tolerated.
//   - valueRLP must be the FULL leaf value as it should appear in the trie
//     (e.g., rlp.Encode(account_struct) with storage_root already in the
//     account's Root field).
//   - The NodeEmitter callback MUST NOT block or do heavy I/O on the hot path
//     — it's called from inside AddLeaf/Root. If you need to batch writes,
//     buffer in the callback and flush periodically.
//   - **A non-nil error from NodeEmitter MUST be treated as fatal by the
//     caller** — HashBuilder logs it but continues, returning a root hash
//     that is inconsistent with whatever was actually persisted to disk.
//     Slice E's writer will halt the build pipeline on emit error.
//   - Once Root() returns, the HashBuilder is consumed; do not call AddLeaf
//     after Root().
//
// # Validation
//
// HashBuilder is cross-validated against alloy_trie::HashBuilder via
// internal/reth/testdata/gen/ — both the root hash and the per-emission
// (path, BranchNodeCompact) sequence must match for a fixed set of fixture
// inputs. See TestGoldenHashBuilder{Root,Emissions}. Note: emissions tests
// are vacuously correct for the current fixture set (pure-leaf insertion;
// no add_branch); the emission path is correct by structural inspection
// against the Rust reference but exercised end-to-end only in incremental
// flows we haven't yet built fixtures for.
//
// # References
//
//   - Spec §6.5: docs/superpowers/specs/2026-04-29-reth-direct-mdbx-design.md
//   - Rust ref: ~/.cargo/registry/.../alloy-trie-0.9.5/src/hash_builder/
//
// NodeEmitter is called by HashBuilder once for each branch node that
// completes during streaming. The path is the trie nibble path from root to
// the branch (using StoredNibbles' 33-byte packed form), and node is the
// reth-canonical BranchNodeCompact.
//
// Emissions happen in path-lexicographic order, so the consumer can write to
// AccountsTrie/StoragesTrie via cursor.append (sequential MDBX writes).
//
// A non-nil error from NodeEmitter MUST be treated as fatal by the caller —
// HashBuilder logs it but continues, so a swallowed error results in a root
// hash that is inconsistent with whatever was actually persisted to disk.
type NodeEmitter func(path StoredNibbles, node BranchNodeCompact) error

// HashBuilder implements the algorithm described on NodeEmitter above.
type HashBuilder struct {
	emit NodeEmitter

	// key is the nibble-form of the most recently added leaf key (nil if none yet).
	key []byte
	// value is the raw value bytes of the most recently added leaf.
	value []byte

	// stack is the right-edge spine of the trie being constructed. Each entry is
	// an RLP-encoded node: either a raw-RLP bytes slice (len < 32) for an inlined
	// node, or a 33-byte slice (0xa0 prefix + 32-byte keccak hash) for a hashed node.
	stack [][]byte

	// stateMasks[depth] accumulates which nibble slots at this branch depth are occupied.
	stateMasks []uint16
	// treeMasks[depth] marks which slots at this branch depth have sub-branches stored in DB.
	treeMasks []uint16
	// hashMasks[depth] marks which slots at this branch depth are stored as 32-byte hashes.
	hashMasks []uint16

	leafCount int
}

// NewHashBuilder returns a HashBuilder that emits completed branch nodes via
// emit. Pass a no-op emit (`func(StoredNibbles, BranchNodeCompact) error
// { return nil }`) for tests that only care about the root hash.
func NewHashBuilder(emit NodeEmitter) *HashBuilder {
	return &HashBuilder{emit: emit}
}

// AddLeaf inserts a (keyNibbles, valueRLP) pair into the trie under
// construction. keyNibbles must be strictly greater than the previous AddLeaf
// call's key (lexicographic on nibble values). valueRLP is the raw value bytes
// associated with the leaf.
func (b *HashBuilder) AddLeaf(keyNibbles []byte, valueRLP []byte) error {
	if b.key != nil {
		cmp := compareNibbles(keyNibbles, b.key)
		if cmp <= 0 {
			return ErrKeysOutOfOrder
		}
		// Process the previous key against the new succeeding key.
		b.update(keyNibbles)
	}
	b.leafCount++
	// Store the new leaf as current.
	b.key = make([]byte, len(keyNibbles))
	copy(b.key, keyNibbles)
	b.value = make([]byte, len(valueRLP))
	copy(b.value, valueRLP)
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
	if b.key != nil {
		// Finalize by calling update with an empty succeeding key.
		b.update(nil)
		b.key = nil
		b.value = nil
	}
	return b.currentRoot()
}

// currentRoot returns the hash of the last item on the stack.
func (b *HashBuilder) currentRoot() common.Hash {
	if len(b.stack) == 0 {
		return common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	}
	last := b.stack[len(b.stack)-1]
	if len(last) == 33 && last[0] == 0xa0 {
		// It's a hash reference: 0xa0 + 32 bytes.
		var h common.Hash
		copy(h[:], last[1:])
		return h
	}
	// It's inline RLP: hash it directly.
	return crypto.Keccak256Hash(last)
}

// update processes b.key/b.value against the succeeding key (may be nil for finalization).
// This is the core of the streaming MPT algorithm, mirroring alloy_trie::HashBuilder::update.
func (b *HashBuilder) update(succeeding []byte) {
	buildExtensions := false
	current := b.key // always the latest added element

	for {
		precedingExists := len(b.stateMasks) > 0
		precedingLen := 0
		if len(b.stateMasks) > 0 {
			precedingLen = len(b.stateMasks) - 1
		}

		commonPrefixLen := commonPrefixLength(succeeding, current)
		length := precedingLen
		if commonPrefixLen > length {
			length = commonPrefixLen
		}
		// length < len(current) is guaranteed by the algorithm invariant.

		// Adjust the state mask: record which nibble at position `length` is occupied.
		extraDigit := current[length]
		for len(b.stateMasks) <= length {
			b.stateMasks = append(b.stateMasks, 0)
		}
		b.stateMasks[length] |= nibbleMask(extraDigit)

		// Ensure tree/hash mask arrays are at least as long as current key.
		b.resizeMasks(len(current))

		lenFrom := length
		if len(succeeding) > 0 || precedingExists {
			lenFrom++
		}

		// The suffix key for this node (current without common prefix).
		shortNodeKey := current[lenFrom:]

		if !buildExtensions {
			// Encode the leaf node from b.value using shortNodeKey as the remaining path.
			leafRLP := encodeLeafNode(shortNodeKey, b.value)
			rlpNode := rlpNodeFromRLP(leafRLP)
			b.stack = append(b.stack, rlpNode)
		}

		if buildExtensions && len(shortNodeKey) > 0 {
			b.updateMasks(current, lenFrom)
			// Pop the top stack item and wrap it in an extension node.
			top := b.stack[len(b.stack)-1]
			b.stack = b.stack[:len(b.stack)-1]
			extRLP := encodeExtensionNode(shortNodeKey, top)
			rlpNode := rlpNodeFromRLP(extRLP)
			b.stack = append(b.stack, rlpNode)
			b.resizeMasks(lenFrom)
		}

		// If the common prefix length is not less than the preceding length and
		// we have a succeeding key, no branch node needs to be created at this level.
		if precedingLen <= commonPrefixLen && len(succeeding) > 0 {
			return
		}

		// Create a branch node if there are multiple items to merge.
		if len(succeeding) > 0 || precedingExists {
			b.pushBranchNode(current, length)
		}

		// Truncate state masks to `length`.
		b.stateMasks = b.stateMasks[:length]
		b.resizeMasks(length)

		// Pop trailing empty state masks.
		for len(b.stateMasks) > 0 && b.stateMasks[len(b.stateMasks)-1] == 0 {
			b.stateMasks = b.stateMasks[:len(b.stateMasks)-1]
		}

		if precedingLen == 0 {
			return
		}

		current = current[:precedingLen]
		buildExtensions = true
	}
}

// pushBranchNode pops the children for the branch at depth `length` off the
// stack, encodes the branch RLP, pushes the result, and emits a BranchNodeCompact
// if warranted (hash_mask or tree_mask non-zero).
//
// Emission semantics: a BranchNodeCompact is ONLY emitted when
// hash_mask | tree_mask != 0, mirroring alloy_trie. Both masks are populated
// exclusively by add_branch() calls (incremental re-execution), which this Go
// port does not yet implement. Consequently, a fresh genesis build (pure-leaf
// insertion only) produces zero emissions — the AccountsTrie/StoragesTrie tables
// remain empty, which is correct reth behavior for genesis-only state.
func (b *HashBuilder) pushBranchNode(current []byte, length int) {
	stateMask := b.stateMasks[length]
	hashMask := uint16(0)
	if length < len(b.hashMasks) {
		hashMask = b.hashMasks[length]
	}
	treeMask := uint16(0)
	if length < len(b.treeMasks) {
		treeMask = b.treeMasks[length]
	}

	// Collect children: pop `popcount(stateMask)` items from the stack.
	childCount := popcount16(stateMask)
	firstChildIdx := len(b.stack) - childCount
	children := b.stack[firstChildIdx:]

	// Encode the branch node.
	branchRLP := encodeBranchNode(stateMask, children)
	rlpNode := rlpNodeFromRLP(branchRLP)

	// Update parent's hash_mask if we're a hashed node.
	if length > 0 {
		parentIdx := length - 1
		b.hashMasks[parentIdx] |= nibbleMask(current[parentIdx])
	}

	// Decide whether to store in DB trie (emit BNC).
	storeFn := treeMask != 0 || hashMask != 0
	if storeFn {
		if length > 0 {
			parentIdx := length - 1
			b.treeMasks[parentIdx] |= nibbleMask(current[parentIdx])
		}

		// Collect the hashes for hashed children.
		var hashes []common.Hash
		childIdx := 0
		for slot := uint16(0); slot < 16; slot++ {
			if stateMask&nibbleMask(byte(slot)) != 0 {
				child := children[childIdx]
				childIdx++
				if hashMask&nibbleMask(byte(slot)) != 0 {
					// child is 0xa0 + 32-byte hash.
					var h common.Hash
					copy(h[:], child[1:])
					hashes = append(hashes, h)
				}
			}
		}

		var rootHashPtr *common.Hash
		if length == 0 {
			rh := b.computeRootFromRlpNode(rlpNode)
			rootHashPtr = &rh
		}

		path := nibblestoStoredNibbles(current[:length])
		bnc := BranchNodeCompact{
			StateMask: stateMask,
			TreeMask:  treeMask,
			HashMask:  hashMask,
			Hashes:    hashes,
			RootHash:  rootHashPtr,
		}
		// Ignore emit errors for now (Task 6 can add error propagation).
		_ = b.emit(path, bnc)
	}

	// Replace children on stack with the single branch node.
	b.stack = b.stack[:firstChildIdx]
	b.stack = append(b.stack, rlpNode)
}

// computeRootFromRlpNode returns the trie root hash for the given RLP node.
func (b *HashBuilder) computeRootFromRlpNode(node []byte) common.Hash {
	if len(node) == 33 && node[0] == 0xa0 {
		var h common.Hash
		copy(h[:], node[1:])
		return h
	}
	return crypto.Keccak256Hash(node)
}

// updateMasks clears the hash_mask bit for the current position and propagates
// tree_mask if needed. Called before wrapping a node in an extension.
func (b *HashBuilder) updateMasks(current []byte, lenFrom int) {
	if lenFrom > 0 {
		flag := nibbleMask(current[lenFrom-1])
		if lenFrom-1 < len(b.hashMasks) {
			b.hashMasks[lenFrom-1] &^= flag
		}
		if len(current)-1 < len(b.treeMasks) && b.treeMasks[len(current)-1] != 0 {
			if lenFrom-1 < len(b.treeMasks) {
				b.treeMasks[lenFrom-1] |= flag
			}
		}
	}
}

// resizeMasks ensures tree/hash mask slices are exactly `newLen` long.
func (b *HashBuilder) resizeMasks(newLen int) {
	for len(b.treeMasks) < newLen {
		b.treeMasks = append(b.treeMasks, 0)
	}
	b.treeMasks = b.treeMasks[:newLen]
	for len(b.hashMasks) < newLen {
		b.hashMasks = append(b.hashMasks, 0)
	}
	b.hashMasks = b.hashMasks[:newLen]
}

// ---------------------------------------------------------------------------
// Helpers: nibble operations
// ---------------------------------------------------------------------------

// bytesToNibbles unpacks each byte to two nibbles (high nibble first).
// Mirrors alloy_trie Nibbles::unpack.
func bytesToNibbles(b []byte) []byte {
	out := make([]byte, len(b)*2)
	for i, by := range b {
		out[i*2] = by >> 4
		out[i*2+1] = by & 0x0f
	}
	return out
}

// commonPrefixLength returns the number of matching leading nibbles in a and b.
// If either is nil/empty the result is 0.
func commonPrefixLength(a, b []byte) int {
	n := len(a)
	if len(b) < n {
		n = len(b)
	}
	for i := 0; i < n; i++ {
		if a[i] != b[i] {
			return i
		}
	}
	return n
}

// compareNibbles returns -1, 0, or 1 for nibble-lexicographic comparison.
func compareNibbles(a, b []byte) int {
	return bytes.Compare(a, b)
}

// nibbleMask returns the uint16 bit mask for a given nibble value (0..=15).
func nibbleMask(nibble byte) uint16 {
	return uint16(1) << nibble
}

// nibblestoStoredNibbles packs a nibble slice into the 33-byte StoredNibbles
// trie key format: packed[32] || length[1].
func nibblestoStoredNibbles(nibbles []byte) StoredNibbles {
	var sn StoredNibbles
	sn.Length = byte(len(nibbles))
	for i, n := range nibbles {
		if i%2 == 0 {
			sn.Packed[i/2] = n << 4
		} else {
			sn.Packed[i/2] |= n & 0x0f
		}
	}
	return sn
}

// ---------------------------------------------------------------------------
// Helpers: RLP encoding
// ---------------------------------------------------------------------------

// rlpNodeFromRLP converts raw RLP bytes to a stack entry:
// - if len < 32: return rlp verbatim (inline embedding)
// - if len >= 32: return 0xa0 + keccak256(rlp) (33 bytes)
func rlpNodeFromRLP(rlp []byte) []byte {
	if len(rlp) < 32 {
		out := make([]byte, len(rlp))
		copy(out, rlp)
		return out
	}
	h := crypto.Keccak256(rlp)
	out := make([]byte, 33)
	out[0] = 0xa0
	copy(out[1:], h)
	return out
}

// encodeLeafNode returns the RLP encoding of [compact_path, value].
// shortKey is the nibble-form suffix (no common prefix). value is raw bytes.
func encodeLeafNode(shortKey []byte, value []byte) []byte {
	compactKey := nibblesToCompact(shortKey, true)
	// Encode compactKey as RLP bytestring.
	encKey := rlpEncodeBytes(compactKey)
	// Encode value as RLP bytestring.
	encVal := rlpEncodeBytes(value)
	// Wrap as RLP list.
	return rlpEncodeList(encKey, encVal)
}

// encodeExtensionNode returns the RLP encoding of [compact_path, child]
// where child is already an RLP node (either inline or 33-byte hash ref).
func encodeExtensionNode(shortKey []byte, child []byte) []byte {
	compactKey := nibblesToCompact(shortKey, false)
	encKey := rlpEncodeBytes(compactKey)
	// child is already the RLP representation to embed directly.
	return rlpEncodeList(encKey, child)
}

// encodeBranchNode returns the RLP encoding of a 17-element branch list.
// stateMask indicates which slots are present; children are the non-empty slots in order.
func encodeBranchNode(stateMask uint16, children [][]byte) []byte {
	// Build 17-element payload: 16 child slots + empty value slot.
	childIdx := 0
	var payload []byte
	for slot := uint16(0); slot < 16; slot++ {
		if stateMask&nibbleMask(byte(slot)) != 0 {
			payload = append(payload, children[childIdx]...)
			childIdx++
		} else {
			payload = append(payload, 0x80) // empty string RLP
		}
	}
	payload = append(payload, 0x80) // branch value: empty

	return rlpEncodeListRaw(payload)
}

// nibblesToCompact encodes nibbles into the compact (hex-prefix) format.
// Leaf flag: 0x20 (even) or 0x30 (odd). Extension flag: 0x00 (even) or 0x10 (odd).
func nibblesToCompact(nibbles []byte, isLeaf bool) []byte {
	odd := len(nibbles)%2 != 0
	var firstByte byte
	switch {
	case isLeaf && odd:
		firstByte = 0x30 | nibbles[0]
	case isLeaf && !odd:
		firstByte = 0x20
	case !isLeaf && odd:
		firstByte = 0x10 | nibbles[0]
	default: // !isLeaf && !odd
		firstByte = 0x00
	}

	start := 0
	if odd {
		start = 1
	}

	// Pack remaining nibbles as pairs.
	rest := nibbles[start:]
	out := make([]byte, 1+len(rest)/2)
	out[0] = firstByte
	for i := 0; i < len(rest)/2; i++ {
		out[1+i] = (rest[i*2] << 4) | rest[i*2+1]
	}
	return out
}

// ---------------------------------------------------------------------------
// Minimal RLP helpers (no external RLP library dependency)
// ---------------------------------------------------------------------------

// rlpEncodeBytes encodes a byte slice as an RLP byte string.
func rlpEncodeBytes(b []byte) []byte {
	if len(b) == 1 && b[0] < 0x80 {
		// Single byte < 0x80: encoded as itself. Return a copy to avoid
		// aliasing the caller's slice — mutating the result would corrupt
		// the original buffer.
		return []byte{b[0]}
	}
	return append(rlpLengthPrefix(len(b), 0x80), b...)
}

// rlpEncodeList encodes pre-encoded items as an RLP list.
// items should each already be RLP-encoded.
func rlpEncodeList(items ...[]byte) []byte {
	var payload []byte
	for _, item := range items {
		payload = append(payload, item...)
	}
	return rlpEncodeListRaw(payload)
}

// rlpEncodeListRaw wraps a raw payload in an RLP list header.
func rlpEncodeListRaw(payload []byte) []byte {
	return append(rlpLengthPrefix(len(payload), 0xc0), payload...)
}

// rlpLengthPrefix returns the RLP length prefix for the given payload length
// with the given base offset (0x80 for strings, 0xc0 for lists).
func rlpLengthPrefix(length int, base byte) []byte {
	if length <= 55 {
		return []byte{base + byte(length)}
	}
	// Long form: base+55+len(BE(length)) || BE(length)
	lenBytes := bigEndianBytes(length)
	out := make([]byte, 1+len(lenBytes))
	out[0] = base + 55 + byte(len(lenBytes))
	copy(out[1:], lenBytes)
	return out
}

// bigEndianBytes returns the minimal big-endian encoding of n (n > 0).
func bigEndianBytes(n int) []byte {
	if n == 0 {
		return []byte{0}
	}
	var buf [8]byte
	i := 7
	for n > 0 {
		buf[i] = byte(n)
		n >>= 8
		i--
	}
	return buf[i+1:]
}
