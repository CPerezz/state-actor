//go:build cgo_reth

package reth

import (
	"context"
	"fmt"

	"github.com/nerolation/state-actor/generator"
)

// runCgoNotAvailableError is nil under -tags cgo_reth. Kept as a symbol so
// TestRunCgoStubBuildPath compiles in both build modes.
var runCgoNotAvailableError error = nil

// RunCgo is the cgo direct-write entry point for --client=reth.
//
// PHASE LAYOUT (full implementation lands across Slice C Tasks 2-6):
//  1. Pre-flight: empty-datadir check, disk-space check
//  2. OpenEnvs (MDBX env + RocksDB)
//  3. WriteDatabaseVersion sidecar
//  4. chainspec.json + WriteMetadata
//  5. Stats + close
//
// Slice C delivers Phases 1-5 with no state writes. Slice D adds state-table
// writes; Slice E adds trie + static-files.
func RunCgo(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	return nil, fmt.Errorf("client/reth.RunCgo: phases not yet implemented (Slice C tasks 2-6)")
}
