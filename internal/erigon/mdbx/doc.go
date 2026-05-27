//go:build cgo_erigon

// Package mdbx writes synthetic state directly into Erigon's MDBX
// chaindata, complementing the `erigon init` pass driven by
// client/erigon.
//
// erigon init seeds the chain config, system tables, sync stages,
// and account/code state from genesis.json. This package then opens
// the same MDBX env and streams PreAlloc + autofill-contract storage
// slots into TblStorageVals and the three storage history tables,
// using genesis-step semantics (txNum=1, step=0).
//
// Why split: erigon init serializes the full Genesis to a single
// mdbx_put under kv.ConfigTable["genesis"], and MDBX's per-value
// limit (~2 GB at 16 KB pages) is exceeded by autofill-contract
// storage at bench scale. Keeping storage out of genesis.json keeps
// init under the limit; this package writes the storage instead.
//
// This package imports only github.com/erigontech/mdbx-go (already
// pulled in by client/reth). It does NOT import erigontech/erigon —
// table-name constants are mirrored as string literals from erigon's
// db/kv/tables.go at the pinned version (internal/erigon/constants.go).
package mdbx
