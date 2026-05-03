// Package besu and its sub-packages mirror the byte-exact subset of Besu 26.5.0
// wire formats that state-actor's Besu writer emits.
//
// Sub-packages:
//   - besu/rlp  — RLP encoders for account state and storage values.
//   - besu/keys — Column-family byte IDs, sentinel keys, BLOCKCHAIN prefix constructors.
//
// The package is dependency-free at the runtime layer: no I/O, no goroutines,
// no database handles. Encoders are unit-testable in isolation against literal
// byte fixtures — the load-bearing strategy for catching wire-format drift
// before it reaches a Besu boot test.
//
// All citations point at hyperledger/besu tag 26.5.0 (May 2026).
package besu

import "github.com/ethereum/go-ethereum/common"

// EmptyTrieNode is the RLP encoding of a null/empty node: a single 0x80 byte.
// Besu defines this at MerkleTrie.java:34 and RLP.java:29 as RLP.NULL.
// This is the "value" stored for the root of an empty sub-trie when inlined.
var EmptyTrieNode = []byte{0x80}

// EmptyTrieNodeHash is keccak256(EmptyTrieNode) = keccak256([0x80]).
// Besu defines this at MerkleTrie.java:35 and uses it at
// BonsaiWorldStateKeyValueStorage.java:286-288 to short-circuit empty-trie writes:
// putAccountStateTrieNode skips the write when hash == EMPTY_TRIE_NODE_HASH.
// Any genesis state root for an account with no storage equals this hash.
var EmptyTrieNodeHash = common.HexToHash(
	"0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421",
)

// EmptyCodeHash is keccak256([]) — the hash of the empty byte string.
// Besu defines it at Hash.java:51 as Hash.EMPTY.
// EOA accounts (no bytecode) store this value in the codeHash field of their
// flat account RLP. No CODE_STORAGE write is made for accounts with this hash
// (BonsaiWorldStateKeyValueStorage.java:248-255).
var EmptyCodeHash = common.HexToHash(
	"0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470",
)

// RlpEmptyList is the RLP encoding of an empty list: a single 0xc0 byte.
// Besu defines this at RLP.java:38-42 as RLP.EMPTY_LIST.
// Used as the receipts RLP for a genesis block (no transactions, no receipts).
var RlpEmptyList = []byte{0xc0}

// LeafTerminator is the nibble appended to all key paths in CompactEncoding
// to signal a leaf node. Besu defines this at CompactEncoding.java:25 as
// LEAF_TERMINATOR = 0x10. The trie builder (internal/besu/trie) uses this
// when HP-encoding leaf paths inside node RLP.
const LeafTerminator byte = 0x10
