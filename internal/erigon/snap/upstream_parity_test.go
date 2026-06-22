package snap

import (
	"path/filepath"
	"testing"
)

// TestDomainTagUpstreamParity pins Domain.Tag() to the exact string
// upstream Erigon's kv.<Domain>.String() returns. If anyone in the
// future "corrects" DomainCommitment.Tag back to "commitments" (the
// previous typo that shipped silent because Erigon's file-discovery
// regex at db/state/dirty_files.go:309-353 just leaves unrecognised
// filenames out of visibleFiles — no warning, no error), this test
// fails immediately at PR time.
//
// Source of truth: upstream
//   - erigontech/erigon/db/state/domain.go:1719,1750
//     (case "commitment": ...)
//   - erigontech/erigon/db/state/aggregator_test.go:474,484
//     (Data(SnapHistory, "commitment", ...))
//   - erigontech/erigon/db/state/merge.go:546
//     (FilenameBase == "commitment")
//
// When PinnedErigonDigest changes, re-verify these strings against the
// new upstream and update this test if upstream's tags change. (They
// have been stable since v3.0 per spot-check of upstream history.)
func TestDomainTagUpstreamParity(t *testing.T) {
	want := map[Domain]string{
		DomainAccounts:   "accounts",
		DomainStorage:    "storage",
		DomainCode:       "code",
		DomainCommitment: "commitment", // NOT "commitments" — see docstring
	}
	for d, w := range want {
		if got := d.Tag(); got != w {
			t.Errorf("Domain(%d).Tag() = %q, upstream expects %q (drift here makes Erigon silently ignore our files)",
				d, got, w)
		}
	}
}

// TestFilenameBuilderUpstreamParity pins the filename pattern
// (Build{Data,BTree,HashMap,Existence}Filename) to match upstream's
// `<version>-<tag>.<from>-<to><ext>` template. Source of truth:
// erigontech/erigon/db/state/snap_schema.go:441,467
// (Sprintf with the same format string).
//
// Catches drift in:
//   - The version prefix ("v1.0-" vs "v2-" or anything else)
//   - The step-range separator ("." vs ":" vs "_")
//   - Zero-padding ("0-1" vs "000000-000001")
//   - Domain tag (caught by TestDomainTagUpstreamParity above too, but
//     this test catches if the tag is wired correctly INTO the
//     filename builder)
//   - Extension strings (".kv" vs ".KV" or ".kvdata")
func TestFilenameBuilderUpstreamParity(t *testing.T) {
	dir := "/snapshots/domain"
	version := "v1.0"
	r := StepRange{From: 0, To: 1}

	cases := []struct {
		name     string
		got      string
		expected string
	}{
		{"accounts.kv", BuildDataFilename(dir, version, DomainAccounts, r), filepath.Join(dir, "v1.0-accounts.0-1.kv")},
		{"accounts.bt", BuildBTreeFilename(dir, version, DomainAccounts, r), filepath.Join(dir, "v1.0-accounts.0-1.bt")},
		{"accounts.kvei", BuildExistenceFilename(dir, version, DomainAccounts, r), filepath.Join(dir, "v1.0-accounts.0-1.kvei")},

		{"storage.kv", BuildDataFilename(dir, version, DomainStorage, r), filepath.Join(dir, "v1.0-storage.0-1.kv")},
		{"storage.bt", BuildBTreeFilename(dir, version, DomainStorage, r), filepath.Join(dir, "v1.0-storage.0-1.bt")},
		{"storage.kvei", BuildExistenceFilename(dir, version, DomainStorage, r), filepath.Join(dir, "v1.0-storage.0-1.kvei")},

		{"code.kv", BuildDataFilename(dir, version, DomainCode, r), filepath.Join(dir, "v1.0-code.0-1.kv")},
		{"code.bt", BuildBTreeFilename(dir, version, DomainCode, r), filepath.Join(dir, "v1.0-code.0-1.bt")},
		{"code.kvei", BuildExistenceFilename(dir, version, DomainCode, r), filepath.Join(dir, "v1.0-code.0-1.kvei")},

		{"commitment.kv", BuildDataFilename(dir, version, DomainCommitment, r), filepath.Join(dir, "v1.0-commitment.0-1.kv")},
		{"commitment.kvi", BuildHashMapFilename(dir, version, DomainCommitment, r), filepath.Join(dir, "v1.0-commitment.0-1.kvi")},
		{"commitment.kvei", BuildExistenceFilename(dir, version, DomainCommitment, r), filepath.Join(dir, "v1.0-commitment.0-1.kvei")},
	}
	for _, c := range cases {
		if c.got != c.expected {
			t.Errorf("%s: got %q, upstream-format expects %q", c.name, c.got, c.expected)
		}
	}
}

// TestAccessorMaskUpstreamParity pins DefaultAccessorMask to the
// upstream per-domain accessor selection. Source of truth:
//   - erigontech/erigon/db/state/statecfg/state_schema.go:197
//     AccountsDomain.Accessors  = AccessorBTree | AccessorExistence
//   - state_schema.go:218 StorageDomain — same
//   - state_schema.go:239 CodeDomain — same
//   - state_schema.go:261 CommitmentDomain.Accessors = AccessorHashMap |
//     AccessorExistence (HashMap, NOT BTree, because commitment branches
//     are nibble-path keys that lookup wants by exact match, not by
//     range-order traversal)
//
// Catches drift if anyone "harmonises" CommitmentDomain to use BTree —
// which Erigon's reader would silently reject because the wrong
// accessor file extension wouldn't match dirty_files.go's expected
// file set for that domain.
func TestAccessorMaskUpstreamParity(t *testing.T) {
	want := map[Domain]AccessorMask{
		DomainAccounts:   AccessorBTree | AccessorExistence,
		DomainStorage:    AccessorBTree | AccessorExistence,
		DomainCode:       AccessorBTree | AccessorExistence,
		DomainCommitment: AccessorHashMap | AccessorExistence,
	}
	for d, w := range want {
		if got := DefaultAccessorMask(d); got != w {
			t.Errorf("DefaultAccessorMask(Domain(%d)) = %d, upstream expects %d", d, got, w)
		}
	}
}
