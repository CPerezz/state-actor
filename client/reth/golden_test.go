//go:build cgo_reth

package reth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/generator"
)

// TestRethGoldenStateRoot pins the state root the full cgo_reth pipeline
// produces for the canonical entitygen MPT fixture
// (seed=12345/10/5/PowerLaw/MaxSlots=100/CodeSize=256). The hash MUST equal
// internal/entitygen.TestCanonicalEntitygenMPTRoot — every entitygen-using
// MPT adapter shares this constant. Drift requires a coordinated update
// across nethermind, besu, reth.
//
// This test is what catches the slot-count RNG drift. Reth previously
// computed `slotCount = (MinSlots+MaxSlots)/2` once outside the RNG draw,
// producing a different state root than besu/nethermind/canonical for any
// non-empty contract alloc. Fixed by drawing slotCount per-contract via
// entitygen.GenerateSlotCount in client/reth/run_cgo.go.
func TestRethGoldenStateRoot(t *testing.T) {
	const expectedRoot = "0xddbfa7c1941ff70fe5a692f7552149adc1ae29ebb2b5dc8bb3544c1368bcb0c3"

	tmpDir := t.TempDir()
	dbPath := filepath.Join(tmpDir, "reth-golden")
	if err := os.MkdirAll(dbPath, 0o755); err != nil {
		t.Fatalf("mkdir: %v", err)
	}

	cfg := generator.Config{
		DBPath:       dbPath,
		NumAccounts:  10,
		NumContracts: 5,
		MaxSlots:     100,
		MinSlots:     1,
		Distribution: generator.PowerLaw,
		Seed:         12345,
		BatchSize:    100,
		Workers:      1,
		CodeSize:     256,
		Verbose:      false,
	}

	stats, err := RunCgo(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("RunCgo: %v", err)
	}
	if stats == nil {
		t.Fatal("RunCgo returned nil stats")
	}
	if stats.StateRoot == (common.Hash{}) {
		t.Fatal("RunCgo returned zero state root — pipeline didn't populate stats.StateRoot")
	}
	if got := stats.StateRoot.Hex(); got != expectedRoot {
		t.Fatalf("reth golden state root mismatch:\n  got:  %s\n  want: %s\n  Diverging here means a coordinated update across all entitygen-using adapters is needed (see internal/entitygen/canonical_mpt_test.go).",
			got, expectedRoot)
	}
}
