//go:build cgo_reth && !linux && !darwin

package reth

import "os"

// apparentSize falls back to logical size on platforms that don't
// expose syscall.Stat_t.Blocks. Reth is Docker-only in practice so
// this branch only fires for local `go build` smoke on Windows
// (which won't run the cgo code anyway because libmdbx isn't built).
func apparentSize(info os.FileInfo) uint64 {
	return uint64(info.Size())
}
