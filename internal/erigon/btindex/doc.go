// Package btindex is a pure-Go implementation of Erigon's `.bt` B+tree
// accessor writer, the lighter-weight alternative to RecSplit used by every
// production value domain (Accounts/Storage/Code per
// db/state/statecfg/state_schema.go:197,218,239). The wire format must be
// byte-identical to Erigon's `db/datastruct/btindex` package; the
// `cross-verify-erigon` CI job regenerates fixtures via the real Erigon
// writer and asserts `bytes.Equal`.
//
// # Spec source
//
// Mirrors `db/datastruct/btindex/btree_index.go` (writer) +
// `db/datastruct/btindex/bps_tree.go` (Node encode/decode) from Erigon
// v3.4.2 (see `internal/erigon/constants.go PinnedErigonCommit`). The
// reference files at
//   - /Users/random_anon/dev/clients/erigon/db/datastruct/btindex/btree_index.go
//   - /Users/random_anon/dev/clients/erigon/db/datastruct/btindex/bps_tree.go
//
// are the spec; line citations `btree_index.go:NNN` / `bps_tree.go:NNN`
// in this package's comments point at those files.
//
// # Wire format (byte layout of a `.bt` file)
//
// The file is either empty (zero keys) or has the following layout:
//
//	┌─────────────────────────────────────────────┐
//	│ EliasFano block (encodes all key offsets)   │
//	│   [8B BE: count-1]                          │
//	│   [8B BE: u = maxOffset + 1]                │
//	│   [N×8B host-LE: data words]                │
//	├─────────────────────────────────────────────┤
//	│ Node list (sparse index of M-th keys)       │
//	│   [8B BE: nodeCount]                        │
//	│   per node:                                 │
//	│     [8B BE: di (key ordinal)]               │
//	│     [2B BE: keyLen]                         │
//	│     [keyLen bytes: key]                     │
//	└─────────────────────────────────────────────┘
//
// The EliasFano block lets the reader translate an ordinal `di` into a
// byte offset in the companion `.kv` file in O(1). The Node list caches
// every M-th key (default M=256, set by `Args.M`) plus the first key if
// the caller's `keep` decision said so — at runtime the reader binary-
// searches over the cached nodes, then within the leaf does a tighter
// binary search using the EliasFano-decoded offsets to fetch the actual
// key from the data file. See `bps_tree.go:NewBpsTreeWithNodes` for the
// read side.
//
// # Node-keep rule (matches Erigon's BuildBtreeIndexWithDecompressor)
//
// On every `AddKey(key, offset)` call this writer decides whether to
// cache the key as a Node:
//
//   - First key (ordinal 0): always kept. Erigon expresses this via a
//     `b0[256]bool` sentinel in BuildBtreeIndexWithDecompressor
//     (`btree_index.go:424-431`) that marks the first key whose top
//     byte is fresh — at ordinal 0 the array starts all-false so the
//     first key is ALWAYS kept. Subsequent fresh-top-byte keys also
//     get `keep=true` from the caller, but Erigon's BtIndexWriter.AddKey
//     OVERWRITES the keep flag at `btree_index.go:241` for any call
//     where `keysWritten > 0`. Net effect: only the first key is
//     b0-kept.
//   - Subsequent keys (ordinal i > 0): kept iff `i % M == 0`. So at
//     M=256 the cached nodes are at di = 0, 256, 512, 768, ...
//
// Encapsulating the b0 logic inside this writer lets callers use the
// simpler `AddKey(key, offset)` API and still get byte-identical output.
//
// # Differences from Erigon's reference writer
//
//   - No `etl.Collector` disk spill: this writer assumes the caller
//     streams offsets in monotonic non-decreasing order (which the
//     production `BuildBtreeIndexWithDecompressor` flow always does
//     because it reads a freshly-built `.kv` file sequentially). The
//     collector in Erigon exists to handle arbitrary input order; since
//     we own the caller contract we require monotonic input and skip
//     the spill path entirely.
//   - No `salt` in Args: Verifier B's correction — salt belongs in the
//     `.kvei` existence-filter pre-hash, not in the BTree itself.
//   - Returns errors instead of panicking on misuse (`AddKey` after
//     `Build`, `Build` twice, etc.).
//
// # Endianness
//
// Erigon's `.bt` file embeds the EliasFano block, which emits its
// `data []uint64` slice in HOST byte order via `unsafe.Slice`. State-
// actor only ships on little-endian targets; the eliasfano package's
// runtime `init()` panics on big-endian platforms so the constraint is
// enforced loudly.
//
// # Concurrency
//
// Writer is single-goroutine; not safe for concurrent AddKey/Build calls.
package btindex
