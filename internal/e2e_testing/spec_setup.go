package e2e_testing

import (
	"path/filepath"
	"testing"

	"github.com/nerolation/state-actor/internal/sizecal"
	"github.com/nerolation/state-actor/internal/spec"
	"github.com/nerolation/state-actor/internal/specbuild"
	"github.com/nerolation/state-actor/internal/templates"
)

// FixedBytesPerSlot is the calibration factor passed to sizecal.NewFixed
// by every per-client TestE2ESuite. Hardcoded (not per-client) so the
// same YAML produces byte-identical PreAlloc across geth/besu/nethermind/
// reth — which is the contract `cross-client-genesis-root` enforces for
// the spec-driven invariant.
//
// 64 B/slot is the geth+besu default in `internal/sizecal/factors.json`;
// reth (60) and nethermind (80) would diverge with sizecal.Default(). For
// CI purposes the calibration value doesn't need to be accurate — it
// just needs to be IDENTICAL across the four clients running the same
// YAML.
const FixedBytesPerSlot uint64 = 64

// CISpecSeed is the seed used by every per-client TestE2ESuite when
// building PreAlloc from the CI fixture. It drives address derivation
// (name-derived + position-derived) AND random-fill synthesis (random
// holder addresses, random balances, random allowance tuples). The
// RPC oracle (CheckERC20Templates) re-derives synthesized values with
// the same seed to verify what landed on-chain.
const CISpecSeed int64 = 0

// LoadCISpecPreAlloc loads the shared CI YAML fixture, validates it,
// translates it through internal/specbuild with sizecal.NewFixed(64),
// and returns the resulting PreAlloc slice for assignment to
// generator.Config.PreAlloc.
//
// Used by every per-client TestE2ESuite to pre-fund the spamoor sender
// plus the suite's flavor entities. The spamoor sender is entity #1 in
// the fixture YAML; if its address drifts from oracle.SpamoorSenderAddr,
// TestCISpecMatchesSpamoorSender (in spec_setup_test.go) fails.
//
// yamlPath is relative to the calling test's cwd — typically
// "../../examples/spec-ci-baseline.yaml" since per-client tests run
// from `client/<name>/`.
func LoadCISpecPreAlloc(t *testing.T, yamlPath, clientName string) []templates.PreAllocEntity {
	t.Helper()
	_, preAlloc := LoadCISpec(t, yamlPath, clientName)
	return preAlloc
}

// LoadCISpec is the broader-shaped variant of LoadCISpecPreAlloc: it
// also surfaces the parsed *spec.Spec so the RPC oracle
// (CheckERC20Templates) can iterate the original entities and verify
// every template parameter via JSON-RPC after the node boots.
func LoadCISpec(t *testing.T, yamlPath, clientName string) (*spec.Spec, []templates.PreAllocEntity) {
	t.Helper()

	absPath, err := filepath.Abs(yamlPath)
	if err != nil {
		t.Fatalf("LoadCISpec: abs path %q: %v", yamlPath, err)
	}
	specDoc, err := spec.ParseFile(absPath)
	if err != nil {
		t.Fatalf("LoadCISpec: ParseFile %q: %v", absPath, err)
	}
	if _, err := specDoc.Validate(templates.UserVisibleNames()); err != nil {
		t.Fatalf("LoadCISpec: Validate: %v", err)
	}

	preAlloc, diag, err := specbuild.Build(specDoc, specbuild.BuildOptions{
		Seed:       CISpecSeed,
		ClientName: clientName,
		Sizer:      sizecal.NewFixed(FixedBytesPerSlot),
	})
	if err != nil {
		t.Fatalf("LoadCISpec: specbuild.Build: %v", err)
	}
	for _, w := range diag.Warnings {
		t.Logf("spec warning: %s", w)
	}
	return specDoc, preAlloc
}
