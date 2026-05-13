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

	absPath, err := filepath.Abs(yamlPath)
	if err != nil {
		t.Fatalf("LoadCISpecPreAlloc: abs path %q: %v", yamlPath, err)
	}
	specDoc, err := spec.ParseFile(absPath)
	if err != nil {
		t.Fatalf("LoadCISpecPreAlloc: ParseFile %q: %v", absPath, err)
	}
	if _, err := specDoc.Validate(templates.UserVisibleNames()); err != nil {
		t.Fatalf("LoadCISpecPreAlloc: Validate: %v", err)
	}

	// sizecal.NewFixed(FixedBytesPerSlot) — uniform across all 4 clients
	// so the same YAML produces the same PreAlloc. The cross-client
	// genesis-root invariant depends on this.
	preAlloc, diag, err := specbuild.Build(specDoc, specbuild.BuildOptions{
		Seed:       0, // address derivation seed; the synthetic-fill seed lives on cfg.Seed
		ClientName: clientName,
		Sizer:      sizecal.NewFixed(FixedBytesPerSlot),
	})
	if err != nil {
		t.Fatalf("LoadCISpecPreAlloc: specbuild.Build: %v", err)
	}
	for _, w := range diag.Warnings {
		t.Logf("spec warning: %s", w)
	}
	return preAlloc
}
