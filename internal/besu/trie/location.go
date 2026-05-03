package trie

// Bonsai location encoding — DB-key derivation for trie nodes.
//
// CRITICAL: This is NOT CompactEncoding. Bonsai DB keys are raw nibble
// sequences with one byte per nibble (values 0x00..0x0F). CompactEncoding
// packs two nibbles per byte and is used only inside node RLP for leaf and
// extension path prefixes (see compact.go).
//
// Source: CommitVisitor.java:46-58 (Besu tag 26.5.0):
//
//	for (int i = 0; i < branchNode.maxChild(); ++i) {
//	    Bytes index = Bytes.of(i);
//	    final Node<V> child = branchNode.child((byte) i);
//	    if (child.isDirty()) {
//	        child.accept(Bytes.concatenate(location, index), this);
//	    }
//	}
//
// Bytes.of(i) produces a single byte with value i in [0..15] — NOT packed.

// RootLocation is the location of the account-trie root: a zero-length slice.
// BonsaiWorldStateKeyValueStorage.saveWorldState writes the root RLP at this
// (empty) key in TRIE_BRANCH_STORAGE.
var RootLocation = []byte{}

// AppendNibble appends one nibble step (for a BranchNode child descent).
// Returns a fresh slice — does not alias the input.
//
// nibble must be in [0, 15]; values outside this range produce an invalid
// location that Besu cannot read.
func AppendNibble(location []byte, nibble byte) []byte {
	out := make([]byte, len(location)+1)
	copy(out, location)
	out[len(location)] = nibble
	return out
}

// AppendPath appends all nibbles of an ExtensionNode path. The input path is
// already nibble-per-byte (each entry in [0..15]).
// Returns a fresh slice — does not alias the input.
func AppendPath(location, path []byte) []byte {
	out := make([]byte, len(location)+len(path))
	copy(out, location)
	copy(out[len(location):], path)
	return out
}

// StorageTrieKey returns the storage-trie node DB key:
//
//	accountHash(32 bytes) ++ location(variable length)
//
// Source: BonsaiWorldStateKeyValueStorage.java:306-309.
//
// For the storage root (location = RootLocation), the result is just the
// 32-byte accountHash.
func StorageTrieKey(accountHash [32]byte, location []byte) []byte {
	out := make([]byte, 32+len(location))
	copy(out, accountHash[:])
	copy(out[32:], location)
	return out
}
