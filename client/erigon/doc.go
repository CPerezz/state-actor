// Package erigon writes a fully-bootable Erigon v3 chaindata directory
// with all synthetic state pre-aged into the cold-tier snapshot files
// (.kv data + .bt BTree accessor + .kvei existence filter for the
// Accounts/Storage/Code domains), bypassing the alternative of actually
// executing 15k+ blocks of EVM transactions to produce equivalent state.
//
// # Approach
//
// Architect B's "long-term-maintainability" path chosen at the project
// planning phase: state-actor does NOT import github.com/erigontech/erigon
// at runtime. Erigon's source tree at /Users/random_anon/dev/clients/erigon/
// is treated as a specification (pinned by commit hash) rather than a
// dependency. File-format primitives are reimplemented in pure Go under
// internal/erigon/:
//
//   - internal/erigon/seg/        — .kv/.v/.ef body writer (Huffman + dict)
//   - internal/erigon/recsplit/   — .kvi RecSplit accessor (commitment-only)
//   - internal/erigon/btindex/    — .bt BTree accessor (value domains)
//   - internal/erigon/existence/  — .kvei FuseFilter v1 existence filter
//   - internal/erigon/eliasfano/  — EliasFano builder (used by recsplit + .ef)
//   - internal/erigon/account/    — EncodeForStorage / DecodeForStorage
//   - internal/erigon/mdbx/       — minimum chaindata MDBX writer
//   - internal/erigon/snap/       — SnapshotWriter API + E3 filename + salt + erigondb.toml
//
// A CI job (`make cross-verify-erigon`) regenerates fixtures via the real
// Erigon binary on the pinned commit and asserts byte-equality, so format
// drift is caught explicitly rather than silently.
//
// # Step semantics
//
// Erigon v3 organizes cold state in `.kv` files spanning [fromStep, toStep)
// txnum ranges. Default step size = 390_625 txnums; default frozen-file
// span = 256 steps → ~100M txnums per frozen file (~20k blocks at ~5k tx/block).
// See db/config3/config3.go:22-34 in the Erigon source tree.
//
// # Build
//
// state-actor's Erigon path uses the `cgo_erigon` build tag for
// Makefile/Dockerfile/CI plug-compatibility with the existing
// cgo_besu/cgo_neth/cgo_reth pattern. The tag is symbolic — the writer
// itself is pure Go (no cgo at the snapshot layer; the chaindata MDBX uses
// erigontech/mdbx-go which is the same dependency reth already carries).
// Vanilla `go build` (the local default) compiles the stub at run_stub.go
// which returns a clear error directing the user at the Dockerfile.
//
//	docker build -f Dockerfile.erigon -t state-actor-erigon .
//	docker run --rm -v $PWD/_artifacts:/data state-actor-erigon \
//	  --client=erigon --db=/data --target-size=500MB --seed=42
//
// # Pinned upstream version
//
// erigontech/erigon:v3.4.2 (snapshot tier file format v1.0).
// Constants live in internal/erigon/constants.go.
//
// # Commitment domain (shipped by default in v1)
//
// V1 ships the Accounts/Storage/Code value domains AND the Commitment
// domain — state-actor owns the HexPatriciaHashed walk and writes the
// commitment .kv files itself. Per Verifier A's review of the planning
// (see /Users/random_anon/.claude/plans/so-i-have-a-declarative-owl.md
// § Critical Verifier Corrections > Correction 1), Erigon's daemon does
// NOT auto-rebuild commitment on first boot: RebuildCommitmentFilesWithHistory
// (db/state/squeeze.go:436) is CLI-only via `cmd/integration commitment
// rebuild`. A datadir missing commitment files will fail boot. State-actor
// must ship commitment.
//
// Options.SkipCommitment is reserved for v2 use cases where the bench host
// pre-rebuilds commitment via `docker run … erigon integration commitment
// rebuild --no-history` as a sidecar step.
package erigon
