package main

import (
	"os"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/ethereum/state-actor/internal/spec"
	"github.com/ethereum/state-actor/internal/templates"
)

// TestGeneratedSpecParsesAndValidates runs the generator end-to-end
// against a fixed seed, then parses + validates the output with the
// project's spec package. Catches schema drift between the generator
// and the parser before we ship the YAML to the remote machine.
func TestGeneratedSpecParsesAndValidates(t *testing.T) {
	if testing.Short() {
		t.Skip("generator emits a 100+ MB YAML; skipping in -short")
	}
	tmp := t.TempDir()
	out := filepath.Join(tmp, "spec.yaml")

	cmd := exec.Command("go", "run", ".", "-out", out, "-seed", "4242")
	cmd.Stderr = os.Stderr
	if err := cmd.Run(); err != nil {
		t.Fatalf("generator: %v", err)
	}

	doc, err := spec.ParseFile(out)
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if _, err := doc.Validate(templates.UserVisibleNames()); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	// Expected counts. The bloated set and bulk counts are target-gb
	// dependent; the test uses the generator's default target (100 GB).
	// At target=100, bloatedSpecForTarget emits 4 of the 5 bloated
	// entities (bloat-30gb-named exceeds the ~30%-of-target budget), and
	// scaledBulkCounts returns the full 15000 EOAs + 200000 contracts.
	// The showcase counts (1 spamoor + 7 erc20 + 2 raw + 12 demo = 22)
	// are fixed. Compute from the generator's own functions so size/
	// target tuning can't silently desync this golden (= 215026 today).
	eoas, contracts := scaledBulkCounts(100)
	want := 1 + len(bloatedSpecForTarget(100)) + 7 + 2 + 12 + eoas + contracts
	if got := len(doc.Entities); got != want {
		t.Errorf("entity count = %d, want %d", got, want)
	}

	// Spamoor sender is the first entity at the canonical address.
	if doc.Entities[0].Address == nil || doc.Entities[0].Address.Address().Hex() !=
		"0x7E5F4552091A69125d5DfCb7b8C2659029395Bdf" {
		t.Errorf("entity 0 not the spamoor sender: addr=%v", doc.Entities[0].Address)
	}
}
