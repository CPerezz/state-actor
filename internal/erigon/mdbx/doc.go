//go:build cgo_erigon

// Package mdbx is state-actor's direct MDBX writer for Erigon
// chaindata. Mirrors Reth's `client/reth/spec_storage_streaming_cgo.go`
// pattern: open the MDBX env, stream-write account/storage/code
// state + commitment trie, patch block 0's header — without going
// through `erigon init`'s in-memory JSON ingest.
//
// Architecture (from /Users/random_anon/.claude/plans/so-i-have-a-declarative-owl.md):
//
//   - Phase A: client/erigon execs `erigon init` with a MINIMAL
//     genesis.json (~100 KB) — bootstraps chain config, system tables,
//     sync stages, ConfigTable.
//   - Phase B: this package's state.go opens MDBX, streams the alloc
//     into TblAccountVals/TblStorageVals/TblCodeVals.
//   - Phase C: this package's commitment.go writes pre-computed
//     commitment branch nodes to TblCommitmentVals (so Erigon's daemon
//     does not recompute on first FCU).
//   - Phase D: this package's header.go patches block 0's header.stateRoot
//     to the value commitment.ComputeGenesisRoot returned.
//
// All files in this package are build-tagged `cgo_erigon` so they only
// compile in the Erigon-targeted build. Combined with cgo_erigon_commitment
// (Phase C only, requires the vendored Erigon HexPatriciaHashed import).
//
// Architect B invariant: this package imports
// `github.com/erigontech/mdbx-go` only (already a state-actor dep
// via client/reth). It does NOT import `github.com/erigontech/erigon` —
// the table-name constants are mirrored as string literals from
// db/kv/tables.go at the pinned commit (internal/erigon/constants.go).
package mdbx
