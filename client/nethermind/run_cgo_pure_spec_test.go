//go:build cgo_neth

package nethermind

import (
	"context"
	"iter"
	"math/big"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/genesis"
	"github.com/nerolation/state-actor/internal/templates"
)

// TestPureSpecDispatchUsesStreamingPath is a regression guard for the
// nethermind dispatch fix. Pre-fix, a Config with NumAccounts==0 and
// NumContracts==0 but non-empty PreAlloc would route to a now-removed
// writeGenesisAllocAccounts handler that read cfg.GenesisStorage —
// which materializePreAlloc no longer populates for spec entities. The
// spec entity's Account.Root stayed at EmptyRootHash, the storage CF
// got no rows, and the state root was wrong (invisible to existing
// CI because every e2e suite passes --accounts > 0, hitting a
// different dispatch branch).
//
// Post-fix, every non-empty input routes through writeSyntheticAccounts,
// whose Phase 0 streams the spec-Storage iter through streamingtrie
// and splices the resulting root into Account.Root.
func TestPureSpecDispatchUsesStreamingPath(t *testing.T) {
	if testing.Short() {
		t.Skip("skipping cgo dispatch test in -short mode")
	}

	g, err := genesis.BuildSynthetic("osaka", big.NewInt(1337), 30_000_000, 0, nil)
	if err != nil {
		t.Fatalf("BuildSynthetic: %v", err)
	}

	addr := common.HexToAddress("0x0000000000000000000000000000000000005555")
	specAccount := &types.StateAccount{
		Nonce:    1,
		Balance:  uint256.NewInt(1_000_000),
		Root:     types.EmptyRootHash,
		CodeHash: types.EmptyCodeHash[:],
	}
	specStorage := map[common.Hash]common.Hash{
		common.HexToHash("0x01"): common.HexToHash("0xaa"),
		common.HexToHash("0x02"): common.HexToHash("0xbb"),
		common.HexToHash("0x03"): common.HexToHash("0xcc"),
	}

	cfg := generator.Config{
		DBPath:       filepath.Join(t.TempDir(), "neth"),
		NumAccounts:  0,
		NumContracts: 0,
		TrieMode:     generator.TrieModeMPT,
		Genesis:      g,
		PreAlloc: []templates.PreAllocEntity{{
			Address: addr,
			Account: specAccount,
			Storage: storageIterFromMap(specStorage),
		}},
	}

	stats, err := Run(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("Run: %v", err)
	}

	// Phase 0 must have spliced the storage root into the spec entity.
	if specAccount.Root == types.EmptyRootHash {
		t.Fatalf("spec entity Root is still EmptyRootHash — Phase 0 spec-storage streaming did not run; "+
			"got Root=%s (cfg.StateRoot=%s)", specAccount.Root.Hex(), stats.StateRoot.Hex())
	}

	// Outer state root is non-zero (the account itself is in state).
	if stats.StateRoot == (common.Hash{}) {
		t.Fatalf("state root is zero hash — dispatch produced no state")
	}

	// Per-spec-entity storage drove the streaming Phase 0; AccountBytes
	// counts the materialised alloc account itself.
	if stats.AccountBytes == 0 {
		t.Errorf("stats.AccountBytes == 0 — alloc account not encoded into state DB")
	}
}

// storageIterFromMap wraps a map in iter.Seq2[common.Hash, common.Hash]
// for use as PreAllocEntity.Storage in tests. Mirrors the helper in
// generator/prealloc_test.go (intentionally duplicated; cross-package
// import would create a test-only dependency cycle).
func storageIterFromMap(m map[common.Hash]common.Hash) iter.Seq2[common.Hash, common.Hash] {
	if len(m) == 0 {
		return nil
	}
	return func(yield func(common.Hash, common.Hash) bool) {
		for k, v := range m {
			if !yield(k, v) {
				return
			}
		}
	}
}
