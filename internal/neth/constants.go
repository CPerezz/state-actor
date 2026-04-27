// Package neth and its sub-packages mirror the byte-exact subset of
// Nethermind's wire formats that state-actor's Nethermind writer emits.
//
// Sub-packages:
//   - neth/rlp     — RLP encoders for header, block, account, BlockInfo,
//                    ChainLevelInfo, receipts.
//   - neth/trie    — TrieNode RLP (branch/extension/leaf) + HexPrefix encoding.
//   - neth/storage — HalfPath / hash-only key construction for the State DB.
//
// The package is **dependency-free at the runtime layer**: no I/O, no
// goroutines, no database handles. That makes encoders unit-testable in
// isolation against literal byte fixtures, which is the load-bearing
// strategy for catching wire-format drift before it reaches a Nethermind
// boot test.
//
// All citations point at Nethermind upstream/master at SHA 09bd5a2d
// (2026-04-26). Updating those citations requires re-pinning the
// state-actor → Nethermind compatibility commit.
package neth

import "github.com/ethereum/go-ethereum/common"

// EmptyTreeHash is keccak256(RLP_empty_string) = keccak256([0x80]).
//
// Nethermind defines this in src/Nethermind/Nethermind.Core/Crypto/Keccak.cs
// as `EmptyTreeHash = InternalCompute([128])`. State trie roots for empty
// allocs MUST equal this constant — Nethermind short-circuits reads at this
// hash without dereferencing the State DB.
var EmptyTreeHash = common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")

// OfAnEmptyString is keccak256(empty bytes) = keccak256([]).
//
// This is the codeHash field of an account that has no code. Nethermind
// uses it as the "no code" sentinel.
var OfAnEmptyString = common.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")

// OfAnEmptySequenceRlp is keccak256(RLP_empty_list) = keccak256([0xc0]).
//
// This is the txRoot / receiptsRoot field of a block with no transactions
// or receipts (the genesis block, by definition).
var OfAnEmptySequenceRlp = common.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347")

// TopStateBoundary is the depth threshold at which Nethermind splits state
// trie nodes between the "top" section (section byte 0) and the "lower"
// section (section byte 1). Paths of length ≤ TopStateBoundary land in
// section 0; deeper paths land in section 1. Storage trie nodes always use
// section byte 2.
//
// From src/Nethermind/Nethermind.Trie/NodeStorage.cs:
//
//	private const int TopStateBoundary = 5;
const TopStateBoundary = 5
