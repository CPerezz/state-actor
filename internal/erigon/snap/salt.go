package snap

import (
	"encoding/binary"
	"fmt"
	"hash/fnv"
	"os"
	"path/filepath"
)

// saltFile is the filename Erigon expects under <datadir>/snapshots/
// (see db/state/aggregator.go — search for `salt-state.txt`).
const saltFile = "salt-state.txt"

// SaltStatePath returns the canonical path for the snapshot salt file
// under <datadir>.
func SaltStatePath(datadir string) string {
	return filepath.Join(SnapshotsDir(datadir), saltFile)
}

// WriteSalt persists salt to <datadir>/snapshots/salt-state.txt as 4
// big-endian bytes (Erigon's format). The directory must already exist
// — call EnsureSnapshotLayout first.
func WriteSalt(datadir string, salt uint32) error {
	var buf [4]byte
	binary.BigEndian.PutUint32(buf[:], salt)
	path := SaltStatePath(datadir)
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, buf[:], 0o644); err != nil {
		return fmt.Errorf("snap.WriteSalt: write tmp: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("snap.WriteSalt: rename: %w", err)
	}
	return nil
}

// ReadSalt reads the 4 BE bytes of <datadir>/snapshots/salt-state.txt.
// Returns os.ErrNotExist if the file is absent.
func ReadSalt(datadir string) (uint32, error) {
	raw, err := os.ReadFile(SaltStatePath(datadir))
	if err != nil {
		return 0, err
	}
	if len(raw) != 4 {
		return 0, fmt.Errorf("snap.ReadSalt: expected 4 bytes, got %d", len(raw))
	}
	return binary.BigEndian.Uint32(raw), nil
}

// DeriveSaltFromSeed produces a deterministic uint32 salt from a
// state-actor run seed. The mapping is FNV-1a over the 8 BE bytes of
// the seed — chosen for: (a) no crypto dep needed, (b) byte-stable
// across Go versions, (c) good distribution.
//
// Two state-actor runs with the same seed produce identical salts;
// snapshot files (including their salt-dependent existence filters)
// are byte-identical run-over-run.
func DeriveSaltFromSeed(seed int64) uint32 {
	h := fnv.New32a()
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], uint64(seed))
	_, _ = h.Write(buf[:])
	return h.Sum32()
}
