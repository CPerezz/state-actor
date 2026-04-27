// Package nethermind writes a fully-bootable Nethermind RocksDB database
// directly, bypassing Nethermind's chainspec loader.
//
// # Approach
//
// Run() opens the RocksDB instances under <datadir>/db/ that Nethermind
// expects (state, code, blocks, headers, blockNumbers, blockInfos,
// receipts), drives entitygen.Source → internal/neth/trie.Builder to
// produce HalfPath-keyed state-trie nodes, then assembles the genesis
// block tree (header / block / blockNumbers / blockInfos with
// WasProcessed=true / empty receipts at row 0). Booting nethermind
// against the produced datadir starts at block 0 ready — no init phase.
//
// # Status — PR#3 stage 1 (this commit)
//
// This package is currently a SCAFFOLDING-ONLY skeleton: Run returns a
// "not yet implemented" error. The full cgo+grocksdb wiring lands in
// PR#3 stage 2 once the librocksdb-dev dependency is provisioned in CI
// and the build-tag gating is finalized. main.go's --client=nethermind
// dispatch surfaces the placeholder error cleanly so users can see where
// the path lives without confusion.
//
// # Pinned target
//
// Nethermind upstream/master at SHA `09bd5a2d` (2026-04-26). RLP shapes
// and HalfPath layouts mirror that exact commit; see internal/neth/.
package nethermind
