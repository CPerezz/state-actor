// Package erigon centralizes the pinned upstream artifacts the
// client/erigon/ writer + oracle tests mirror. Bumping any of these
// requires regenerating the Erigon-cross fixtures under
// internal/erigon/{seg,recsplit,existence,eliasfano,account,mdbx,btindex,snap}/testdata/
// and running the full oracle suite (`make test-erigon-suite`).
package erigon

const (
	// PinnedErigonCommit is the exact Erigon source SHA whose file formats
	// + MDBX bucket schema this package mirrors. Captured from the local
	// reference checkout at /Users/random_anon/dev/clients/erigon/ at the
	// point the pure-Go reimplementation was started.
	//
	// Bumping this constant requires:
	//   1. git pull in /Users/random_anon/dev/clients/erigon/
	//   2. capture new HEAD SHA
	//   3. regenerate every internal/erigon/*/testdata/ fixture via the
	//      build-tagged `erigon_gen` shim binaries
	//   4. re-run `make cross-verify-erigon` until green
	//
	// First-write value pinned to the v3.4.2 tag commit. To be replaced
	// with the actual `git rev-parse v3.4.2^{commit}` once the worktree
	// is checked out at the tag rather than main.
	PinnedErigonCommit = "v3.4.2"

	// PinnedErigonImage is the fully-qualified Docker image reference
	// (registry + name) without the tag. Erigon Technologies publishes
	// to Docker Hub under this name.
	PinnedErigonImage = "erigontech/erigon"

	// PinnedErigonRelease is the Docker image tag the oracle suite +
	// e2e tests + scripts/run-bloatnet.sh boot against.
	//
	// Currently the human-readable tag only; the content digest pin
	// (sha256@...) will be appended once we run
	// `docker inspect erigontech/erigon:v3.4.2 --format '{{index .RepoDigests 0}}'`
	// on the bench host or local machine and freeze the resulting digest.
	// See PinnedErigonDigest below for the placeholder.
	PinnedErigonRelease = "v3.4.2"

	// PinnedErigonDigest is the sha256 content digest of the Erigon
	// image at PinnedErigonRelease. Captured 2026-05-26 via:
	//   docker pull erigontech/erigon:v3.4.2
	//   docker inspect erigontech/erigon:v3.4.2 \
	//     --format '{{index .RepoDigests 0}}'
	//
	// Oracle tests / Dockerfile / run-bloatnet.sh prefer `image@digest`
	// over `image:tag` for reproducibility — matches the reth pattern at
	// internal/reth/constants.go:41. Tag-only `erigontech/erigon:v3.4.2`
	// resolves to the SAME image as of capture but Docker Hub may
	// retag `v3.4.2` in the future (Erigon historically does this for
	// security backports), at which point the digest pin is the only
	// reproducible reference.
	//
	// Bump procedure:
	//   1. docker pull erigontech/erigon:vX.Y.Z
	//   2. docker inspect ... --format '{{index .RepoDigests 0}}'
	//   3. Update PinnedErigonRelease + PinnedErigonDigest together.
	//   4. Regenerate internal/erigon/_fixtures/ artifacts (`make regen-erigon-fixtures`).
	//   5. Re-run `make test-erigon-suite` and `make bench-erigon-25gb`.
	PinnedErigonDigest = "sha256:bac6b266367ec59f82576cd8cef047171601fce3f0bbbef36e3e4db24727ff6e"

	// PinnedMdbxGoVer is the github.com/erigontech/mdbx-go module version
	// the chaindata writer links against. Pinned to match state-actor's
	// existing pin from internal/reth/constants.go:52, NOT Erigon's
	// upstream v0.40.1 — keeping a single mdbx-go version across all
	// state-actor clients avoids C-ABI conflicts inside one binary.
	//
	// The SetGeometry / SetOption / OpenDBI APIs we touch are stable
	// across v0.38.4 ↔ v0.40.1, so reading an Erigon-written DB with
	// our v0.38.4 build (or vice versa) works at the binding level.
	PinnedMdbxGoVer = "v0.38.4"

	// SnapshotFormatVersion is the schema version we emit in snapshot
	// filenames (the `v1.0-` prefix in `v1.0-accounts.0-256.kv`). Tracks
	// db/state/statecfg/version_schema_gen.go:9-11 — DataKV's MinSupported
	// is v1.0 (Current is v2.0 as of v3.4.2). We deliberately pin to the
	// MinSupported value so older Erigon binaries (pre-v3.4) can also
	// read the produced datadir.
	SnapshotFormatVersion = "v1.0"

	// StepSize is the default Erigon step size in txnums (per
	// db/config3/config3.go:29). state-actor uses this exact value in
	// erigondb.toml so step boundaries align with Erigon's expectations.
	StepSize uint64 = 390_625

	// StepsInFrozenFile is the default Erigon steps-per-frozen-file
	// (per db/config3/config3.go:34). 256 steps × 390_625 txnums =
	// 100_000_000 txnums ≈ 20k blocks at ~5k tx/block.
	StepsInFrozenFile uint64 = 256
)
