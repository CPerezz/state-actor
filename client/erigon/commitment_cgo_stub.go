//go:build cgo_erigon && !cgo_erigon_commitment

// Empty stub package file — all formerly-stubbed functions
// (patchGenesisHeaderStateRoot) moved their stubs into snapshot_cgo_stub.go
// alongside writeSnapshots, which is the only call site under the
// !cgo_erigon_commitment build. Kept as a marker file so build tags
// align with commitment_cgo.go.

package erigon
