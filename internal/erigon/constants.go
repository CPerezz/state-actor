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

	// PinnedMdbxGoVer is the mdbx-go binding version the writer links
	// against. Matches internal/reth/constants.go so a single C-ABI
	// version is shared across clients in one binary.
	PinnedMdbxGoVer = "v0.38.4"
)
