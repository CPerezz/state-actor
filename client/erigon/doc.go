// Package erigon writes a bootable Erigon v3 chaindata directory by
// driving the pinned `erigon init` CLI against a synthetic genesis.json
// (Phase A) and then writing PreAlloc + autofill-contract storage slots
// directly into MDBX via internal/erigon/mdbx (Phase B).
//
// The Phase B direct-MDBX path exists because Erigon's
// WriteGenesisIfNotExist serializes the full *types.Genesis to a single
// mdbx_put under kv.ConfigTable["genesis"], and MDBX's per-value limit
// (~2 GB at 16 KB pages) is exceeded by autofill-contract storage at
// bench scale. Splitting that storage out of genesis.json keeps the
// JSON blob small enough for erigon init, and Phase B streams the slots
// chunk-by-chunk into TblStorageVals + the three storage history tables
// using genesis-step semantics (txNum=1, step=0).
//
// Build: requires the cgo_erigon tag, which pulls in
// github.com/erigontech/mdbx-go. Without the tag, run_stub.go returns a
// clear error directing the user at Dockerfile.erigon.
//
//	docker build -f Dockerfile.erigon -t state-actor-erigon .
//	docker run --rm -v $PWD/_artifacts:/data state-actor-erigon \
//	  --client=erigon --db=/data --target-size=500MB --seed=42
//
// Pinned upstream image: see internal/erigon/constants.go.
package erigon
