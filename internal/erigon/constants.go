// Package erigon pins the upstream Erigon Docker image the client/erigon/
// writer + bench script boot against.
package erigon

const (
	// PinnedErigonImage is the registry-qualified Erigon image name.
	PinnedErigonImage = "erigontech/erigon"

	// PinnedErigonRelease is the human-readable tag the bench uses.
	PinnedErigonRelease = "v3.4.2"

	// PinnedErigonDigest is the sha256 content digest of the image at
	// PinnedErigonRelease. The bench prefers `image@digest` over
	// `image:tag` for reproducibility — Docker Hub may retag for
	// security backports, at which point the digest is the only stable
	// reference. Bump via `docker inspect erigontech/erigon:vX.Y.Z
	// --format '{{index .RepoDigests 0}}'`.
	PinnedErigonDigest = "sha256:bac6b266367ec59f82576cd8cef047171601fce3f0bbbef36e3e4db24727ff6e"

	// SnapshotFormatVersion is the schema version we emit in snapshot
	// filenames (the `v1.0-` prefix in `v1.0-accounts.0-256.kv`). Tracks
	// db/state/statecfg/version_schema_gen.go — DataKV's MinSupported is
	// v1.0 (Current is v2.0 as of v3.4.2). We pin to MinSupported so
	// older Erigon binaries can also read the produced datadir.
	SnapshotFormatVersion = "v1.0"

	// StepSize is the default Erigon step size in txnums (per
	// db/config3/config3.go:29). state-actor writes this exact value
	// into erigondb.toml so step boundaries align with Erigon's
	// expectations.
	StepSize uint64 = 390_625

	// StepsInFrozenFile is the default Erigon steps-per-frozen-file
	// (per db/config3/config3.go:34). 256 steps × 390_625 txnums =
	// 100_000_000 txnums ≈ 20k blocks at ~5k tx/block.
	StepsInFrozenFile uint64 = 256
)
