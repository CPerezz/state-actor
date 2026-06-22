//go:build cgo_erigon_commitment

// Package commitment is a thin adapter over Erigon's
// `execution/commitment` HexPatriciaHashed trie engine. State-actor uses
// it to compute the genesis stateRoot that Erigon's daemon also computes
// on first boot — the value Erigon reports via
// `eth_getBlockByNumber(0).stateRoot` after `erigon init`.
//
// HexPatriciaHashed is driven entirely through the `PatriciaContext`
// interface (Branch / PutBranch / Account / Storage), so state-actor
// supplies its own context and routes the branch bytes itself — no Erigon
// DB is required. We vendor the trie engine rather than re-port it because
// a faithful HexPatriciaHashed reimplementation is large and error-prone;
// the cost is that the import transitively pulls a big module graph, which
// the `cgo_erigon_commitment` build tag keeps out of the default
// `go build ./...`.
//
// This package is the ONLY place in state-actor that imports
// `github.com/erigontech/erigon`. To keep the default build free of that
// dependency, do NOT import this package from a non-build-tagged file.
package commitment
