package btindex

import (
	"fmt"
	"os"
	"path/filepath"
)

// createTempFile creates a temp file next to the final destination and
// returns its (path, *File). Mirrors `dir.CreateTemp(file)` at
// /Users/random_anon/dev/clients/erigon/common/dir/rw_dir.go:230-233,
// which itself wraps `os.CreateTemp(dir, "<basename>.*.tmp")`.
//
// The random infix is irrelevant to the wire format — the file is
// renamed to its final name once flushed. Erigon's restart logic
// cleans up stray `.tmp` files on boot, so we follow the same suffix
// convention even though state-actor doesn't currently implement
// that cleanup pass (the orchestrator at client/erigon/ will).
func createTempFile(finalPath string) (string, *os.File, error) {
	dir := filepath.Dir(finalPath)
	base := filepath.Base(finalPath)
	pattern := fmt.Sprintf("%s.*.tmp", base)
	f, err := os.CreateTemp(dir, pattern)
	if err != nil {
		return "", nil, err
	}
	// os.CreateTemp hardcodes mode 0o600 regardless of umask. The
	// downstream Erigon daemon runs as a different uid and silently
	// fails to open files it can't read — chmod to 0o644 so the
	// renamed final .bt is world-readable. See bd85125 / 05c3ebd
	// for the broader context.
	if err := f.Chmod(0o644); err != nil {
		_ = f.Close()
		_ = os.Remove(f.Name())
		return "", nil, fmt.Errorf("btindex: chmod temp: %w", err)
	}
	return f.Name(), f, nil
}
