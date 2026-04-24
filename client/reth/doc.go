// Package reth populates a Reth-compatible database from state-actor's
// generator output.
//
// # How it works
//
// Reth's database format (MDBX + 30 tables + custom "Compact" binary codec +
// AccountsTrie/StoragesTrie emission) is not practical to write directly from
// Go for this first iteration. Instead, this package uses Reth's own
// `reth init-state` CLI subcommand, which accepts a JSONL state dump:
//
//	line 1:  {"root":"<state-root-hex>"}
//	line N:  {"address":"0x..", "balance":"0x..", "nonce":N, "code":"0x..",
//	          "storage":{"0x..":"0x..", ...}}
//
// This package:
//  1. Generates accounts / contracts / storage slots deterministically from
//     the config's seed.
//  2. Streams each account as a JSONL line to a temp file.
//  3. Computes the Merkle-Patricia state root in Go using go-ethereum's
//     StackTrie primitives, writes it as line 1.
//  4. Invokes `reth init-state <jsonl> --chain <genesis.json> --datadir <path>`.
//
// The advantages over a direct MDBX writer:
//   - Correctness: Reth writes its own DB, so schema/codec drift is handled
//     by Reth upstream.
//   - Self-contained: the package only depends on go-ethereum, os/exec, and
//     the reth binary being in PATH.
//   - Simple: ~500 lines of Go instead of ~3500.
//
// The disadvantages:
//   - Requires the `reth` binary in PATH at generation time.
//   - JSONL streaming is slower than direct MDBX for very large states
//     (tens of millions of accounts). This is acceptable for devnet testing,
//     which is state-actor's primary use case.
//   - The `--target-size` flag of the geth path is not honored; see Populate
//     for the supported subset of config flags.
//
// # Self-contained
//
// Per the project's multi-client design, this package duplicates the
// RNG-driven entity-generation primitives from the generator/ package rather
// than depending on internal generator types. This keeps client/reth/
// self-contained and mirrors how future client packages (erigon, besu,
// nethermind) will be organized.
package reth
