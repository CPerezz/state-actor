//go:build cgo_erigon && cgo_erigon_commitment

package erigon

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"

	"github.com/ethereum/state-actor/generator"
	"github.com/ethereum/state-actor/internal/autofill"
)

// TestParallelEquivalence proves writeSnapshots produces an identical
// HPH root regardless of the Phase 1 encode-worker pool size.
//
// The HPH root is the only externally-visible signal that all clients
// (geth, reth, besu, nethermind, erigon) MUST agree on — it gets
// patched into block 0's header.stateRoot and gated by the bench's
// MATCH/DIVERGE check. If two runs of writeSnapshots with the same
// SEED produce the same root, the parallel pipeline is byte-equivalent
// to a serial one for cross-client invariance purposes.
//
// Mechanism: streamsort.Put is order-insensitive (Pebble LSM sorts
// by key internally), so the per-domain channels' arrival-order
// non-determinism cannot affect the sorted commitmentInputStore
// content. ComputeGenesisRoot is deterministic over identical store
// content. Therefore identical input → identical root regardless of N.
//
// Scale: ~50 MiB autofill budget → ~57 K EOAs + ~100 contracts +
// ~35 K storage slots. Single-machine runtime ~5-10 s per run.
func TestParallelEquivalence(t *testing.T) {
	rootSerial := runWriteSnapshots(t, 1)
	rootParallel := runWriteSnapshots(t, 8)

	if rootSerial != rootParallel {
		t.Fatalf("HPH root diverges between worker counts:\n  N=1: %s\n  N=8: %s",
			rootSerial.Hex(), rootParallel.Hex())
	}
}

// runWriteSnapshots executes writeSnapshots in an isolated tmp dir
// with the requested encode-worker pool size and returns the HPH root.
func runWriteSnapshots(t *testing.T, workers int) common.Hash {
	t.Helper()

	restore := setErigonWorkers(workers)
	defer restore()

	plan, err := autofill.PlanForBudget(50 * 1024 * 1024)
	if err != nil {
		t.Fatalf("autofill.PlanForBudget: %v", err)
	}

	// One sentinel spec account so foundational isn't empty — the
	// spec ingest path through the channel pipeline gets exercised
	// on every run (not just the autofill loop).
	specAddr := common.HexToAddress("0x000000000000000000000000000000000000beef")
	cfg := generator.Config{
		DBPath:     t.TempDir(),
		Seed:       42,
		AutoFill:   plan,
		TargetSize: 50 * 1024 * 1024,
		GenesisAccounts: map[common.Address]*types.StateAccount{
			specAddr: {Nonce: 1, Balance: uint256.NewInt(1234)},
		},
	}

	foundational, stats, err := buildAllocMap(cfg)
	if err != nil {
		t.Fatalf("buildAllocMap: %v", err)
	}

	root, err := writeSnapshots(context.Background(), cfg, foundational, stats)
	if err != nil {
		t.Fatalf("writeSnapshots (workers=%d): %v", workers, err)
	}
	return root
}
