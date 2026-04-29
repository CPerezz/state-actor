// Package reth and its sub-packages mirror the byte-exact subset of reth's
// MDBX schema and Compact codec that state-actor's reth direct-writer
// emits.
//
// The package is dependency-free at the runtime layer: no I/O, no
// goroutines, no database handles. That makes encoders unit-testable in
// isolation against literal byte fixtures, which is the load-bearing
// strategy for catching wire-format drift before it reaches a reth boot
// test.
//
// All citations point at reth upstream at the SHA recorded in
// PinnedRethCommit. Updating those citations requires re-pinning the
// state-actor → reth compatibility commit and regenerating
// testdata/fixtures.json.
package reth
