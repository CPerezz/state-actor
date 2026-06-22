package snap

import (
	"fmt"
	"os"
	"path/filepath"
)

// dbSettingsFile is the filename Erigon writes/reads at the snapshots
// root containing the step-size + steps-in-frozen-file invariants
// (see db/state/aggregator.go — search for `erigondb.toml`).
const dbSettingsFile = "erigondb.toml"

// ErigonDBSettingsPath returns the canonical path for erigondb.toml.
func ErigonDBSettingsPath(datadir string) string {
	return filepath.Join(SnapshotsDir(datadir), dbSettingsFile)
}

// WriteErigonDBSettings persists the step parameters as a 2-line TOML
// at <datadir>/snapshots/erigondb.toml. Hand-formatted (no TOML lib
// dep) — Erigon's parser at `ResolveErigonDBSettings` is lenient and
// accepts the format we emit verbatim.
func WriteErigonDBSettings(datadir string, stepSize, stepsInFrozenFile uint64) error {
	body := fmt.Sprintf("step_size = %d\nsteps_in_frozen_file = %d\n", stepSize, stepsInFrozenFile)
	path := ErigonDBSettingsPath(datadir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, []byte(body), 0o644); err != nil {
		return fmt.Errorf("snap.WriteErigonDBSettings: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("snap.WriteErigonDBSettings: rename: %w", err)
	}
	return nil
}
