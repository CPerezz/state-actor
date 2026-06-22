package snap

import (
	"fmt"
	"os"
	"path/filepath"
)

// Snapshot subdirectory layout under <datadir>:
//
//	snapshots/
//	├── domain/     ← .kv + .bt + .kvi + .kvei (this writer's targets)
//	├── history/    ← .v   (history values; v2 feature; we don't emit)
//	├── idx/        ← .efi (inverted-index history accessors; v2)
//	└── accessor/   ← (Erigon's catch-all for cross-domain accessors)
//
// Per erigontech/erigon/db/state/aggregator.go (search
// `Dirs` setup) and erigon's `datadir.New` directory layout.
const (
	subDirSnapshots = "snapshots"
	subDirDomain    = "domain"
	subDirHistory   = "history"
	subDirIdx       = "idx"
	subDirAccessor  = "accessor"
)

// SnapshotsDir returns <datadir>/snapshots — the root for all snapshot
// artifacts.
func SnapshotsDir(datadir string) string {
	return filepath.Join(datadir, subDirSnapshots)
}

// DomainDir returns <datadir>/snapshots/domain — where this writer
// emits its files.
func DomainDir(datadir string) string {
	return filepath.Join(SnapshotsDir(datadir), subDirDomain)
}

// HistoryDir, IdxDir, AccessorDir round out the layout — empty in v1.
func HistoryDir(datadir string) string  { return filepath.Join(SnapshotsDir(datadir), subDirHistory) }
func IdxDir(datadir string) string      { return filepath.Join(SnapshotsDir(datadir), subDirIdx) }
func AccessorDir(datadir string) string { return filepath.Join(SnapshotsDir(datadir), subDirAccessor) }

// EnsureSnapshotLayout creates all four snapshot subdirectories under
// <datadir> with 0o755 perms. Idempotent — safe to call on an existing
// layout.
func EnsureSnapshotLayout(datadir string) error {
	for _, d := range []string{DomainDir(datadir), HistoryDir(datadir), IdxDir(datadir), AccessorDir(datadir)} {
		if err := os.MkdirAll(d, 0o755); err != nil {
			return fmt.Errorf("snap.EnsureSnapshotLayout: mkdir %q: %w", d, err)
		}
	}
	return nil
}
