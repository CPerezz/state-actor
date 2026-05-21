package e2e_testing

import (
	"context"
	"testing"

	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/entitygen"
	"github.com/nerolation/state-actor/internal/oracle"
)

// GoldenStateRootCfg returns the canonical Osaka-bootable config every
// MPT-mode client adapter must produce the same state root for: 10 EOAs +
// 5 contracts (seed=12345, PowerLaw, MaxSlots=100, CodeSize=256). Callers
// supply DBPath and may set TrieMode / WriteTrieNodes / Verbose before
// passing the cfg to AssertGoldenStateRoot.
func GoldenStateRootCfg(dbPath string) generator.Config {
	return generator.Config{
		DBPath:       dbPath,
		NumAccounts:  10,
		NumContracts: 5,
		MaxSlots:     100,
		MinSlots:     1,
		Distribution: generator.PowerLaw,
		Seed:         12345,
		CodeSize:     256,
	}
}

// AssertGoldenStateRoot adds the 4 canonical EIP system contracts to cfg,
// invokes runFn, and asserts the produced state root equals
// entitygen.CanonicalOsakaMPTRoot. Drift requires a coordinated update of
// CanonicalOsakaMPTRoot + every caller + internal/entitygen/canonical_mpt_test.go.
func AssertGoldenStateRoot(t *testing.T, clientName string, cfg generator.Config,
	runFn func(context.Context, generator.Config) (*generator.Stats, error)) {
	t.Helper()
	oracle.AddCanonicalSystemContracts(&cfg)

	stats, err := runFn(context.Background(), cfg)
	if err != nil {
		t.Fatalf("%s run: %v", clientName, err)
	}
	if stats == nil {
		t.Fatalf("%s run returned nil stats", clientName)
	}
	if stats.StateRoot == (common.Hash{}) {
		t.Fatalf("%s run returned zero state root", clientName)
	}
	want := entitygen.CanonicalOsakaMPTRoot.Hex()
	if got := stats.StateRoot.Hex(); got != want {
		t.Fatalf("%s golden state root mismatch:\n  got:  %s\n  want: %s\n  Drift requires coordinated update of entitygen.CanonicalOsakaMPTRoot + all 4 client goldens + internal/entitygen/canonical_mpt_test.go.",
			clientName, got, want)
	}
}
