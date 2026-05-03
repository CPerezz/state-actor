package reth

// Pinned versions of the upstream reth artifacts we mirror. Bumping any of
// these requires regenerating testdata/fixtures.json (see testdata/README.md)
// and, once it lands, running the differential oracle that will live at
// client/reth/oracle_test.go (Slice F scope).
const (
	// PinnedRethCommit is the exact reth source SHA whose schema and codec
	// this package mirrors.
	PinnedRethCommit = "6fa48a497a"

	// PinnedCodecsVer is the reth-codecs crate.io version whose Compact
	// encoding this package reproduces.
	PinnedCodecsVer = "0.3.1"

	// PinnedAlloyTrieVer is the alloy-trie crate.io version whose
	// BranchNodeCompact format this package reproduces.
	PinnedAlloyTrieVer = "0.9.5"

	// PinnedRethRelease is the Docker image tag the differential oracle
	// boots against in CI. Bump when validating against a new release.
	//
	// SHA-pinned to the CPerezz/reth fork branch `skip-genesis-validation`
	// while paradigmxyz/reth is reviewing the upstream PR for that flag.
	// The fork's GHA workflow (.github/workflows/build-and-push-on-push.yml)
	// auto-publishes per-commit Docker images on every push to that branch.
	// Once the upstream PR merges and a release is cut, flip this back to
	// `vX.Y.Z` and `PinnedRethImage` back to ghcr.io/paradigmxyz/reth.
	PinnedRethRelease = "00b3cbb5f2e31af0f13ec234dd6956f7e2e49225"

	// PinnedRethImage is the fully-qualified image reference (registry + name)
	// without the tag. Reth is published to GHCR.
	PinnedRethImage = "ghcr.io/cperezz/reth"

	// PinnedMdbxGoVer is the github.com/erigontech/mdbx-go module version
	// that the cgo writer links against. Pinned because libmdbx C ABI is
	// version-sensitive.
	PinnedMdbxGoVer = "v0.38.4"

	// DBVersion is the value written to <datadir>/db/database.version. Reth
	// boot validates this exact value; mismatch fails fast.
	DBVersion = 2
)
