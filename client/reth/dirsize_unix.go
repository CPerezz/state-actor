//go:build cgo_reth && (linux || darwin)

package reth

import (
	"os"
	"syscall"
)

// apparentSize returns the actual disk-allocated bytes for a file,
// looking through sparse-file preallocation. MDBX preallocates
// mdbx.dat in 4 GiB growth steps but only writes a small fraction
// of those blocks during state-actor's run, so reading the logical
// info.Size() would over-report by ~20x at small --target-size
// values. syscall.Stat_t.Blocks reports the 512-byte blocks
// actually committed to disk, which tracks real data volume.
//
// Linux + macOS both expose Stat_t.Blocks. Windows uses
// dirsize_other.go which falls back to logical size (reth is
// Docker-only in practice, so this fallback only matters for local
// `go build` smoke).
func apparentSize(info os.FileInfo) uint64 {
	if stat, ok := info.Sys().(*syscall.Stat_t); ok {
		return uint64(stat.Blocks) * 512
	}
	return uint64(info.Size())
}
