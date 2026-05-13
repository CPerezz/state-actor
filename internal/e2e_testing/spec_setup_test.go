package e2e_testing

import (
	"strings"
	"testing"

	"github.com/nerolation/state-actor/internal/oracle"
)

// TestCISpecMatchesSpamoorSender asserts the spamoor sender entity in
// examples/spec-ci-baseline.yaml uses the exact address constant from
// internal/oracle/devkeys.go. The YAML and the constant must stay in
// sync or spamoor will sign txs from an unfunded address and the CI
// suite will fail mysteriously at Phase 5.
//
// Runs in the default CI job (no build tags) so PRs that update one
// without the other surface immediately.
func TestCISpecMatchesSpamoorSender(t *testing.T) {
	// Load the canonical CI YAML. From this test (running in
	// internal/e2e_testing/), the fixture is at ../../examples/.
	preAlloc := LoadCISpecPreAlloc(t, "../../examples/spec-ci-baseline.yaml", "geth")

	wantAddr := oracle.SpamoorSenderAddr
	for _, pe := range preAlloc {
		if pe.Address == wantAddr {
			// Found it. Sanity-check the balance has at least 17 zeros
			// (≈1 ETH) — guards against someone reducing the balance
			// while keeping the address.
			balStr := pe.Account.Balance.String()
			if !strings.HasSuffix(balStr, "000000000000000000") {
				t.Errorf("spamoor sender balance %s lacks 18-zero tail (likely under-funded)", balStr)
			}
			return
		}
	}
	t.Fatalf("examples/spec-ci-baseline.yaml has no entity at oracle.SpamoorSenderAddr (%s); "+
		"the YAML and devkeys.go drifted apart. Restore the entity or update the YAML.", wantAddr.Hex())
}
