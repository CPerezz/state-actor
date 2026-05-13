package generator

import (
	"iter"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/internal/templates"
)

// TestPreAllocShimMaterializes verifies Validate() folds Config.PreAlloc
// entries into the legacy GenesisAccounts/Code/Storage maps so v1 client
// writers can consume them without changing.
func TestPreAllocShimMaterializes(t *testing.T) {
	addr := common.HexToAddress("0x000000000000000000000000000000000000aaaa")

	cfg := &Config{
		PreAlloc: []templates.PreAllocEntity{{
			Address: addr,
			Account: &types.StateAccount{
				Nonce:    1,
				Balance:  uint256.NewInt(1000),
				Root:     types.EmptyRootHash,
				CodeHash: types.EmptyCodeHash[:],
			},
			Code: []byte{0x60, 0x80},
			Storage: storageMap(map[common.Hash]common.Hash{
				common.HexToHash("0x01"): common.HexToHash("0xaa"),
				common.HexToHash("0x02"): common.HexToHash("0xbb"),
			}),
		}},
	}

	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}

	if got := cfg.GenesisAccounts[addr]; got == nil {
		t.Fatal("shim did not materialize account into GenesisAccounts")
	}
	if got := cfg.GenesisCode[addr]; len(got) != 2 {
		t.Errorf("shim did not materialize code: got %v", got)
	}
	if got := cfg.GenesisStorage[addr]; len(got) != 2 {
		t.Errorf("shim did not materialize storage: got %v entries, want 2", len(got))
	}

	// PreAlloc must be cleared so a second Validate() call is idempotent.
	if cfg.PreAlloc != nil {
		t.Errorf("Validate should clear PreAlloc; got %d entries left", len(cfg.PreAlloc))
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("second Validate must succeed (idempotent): %v", err)
	}
}

func TestPreAllocShimRejectsCollisionWithGenesisAccounts(t *testing.T) {
	addr := common.HexToAddress("0x000000000000000000000000000000000000bbbb")
	cfg := &Config{
		GenesisAccounts: map[common.Address]*types.StateAccount{
			addr: {Nonce: 0, Balance: uint256.NewInt(0), Root: types.EmptyRootHash, CodeHash: types.EmptyCodeHash[:]},
		},
		PreAlloc: []templates.PreAllocEntity{{
			Address: addr,
			Account: &types.StateAccount{Nonce: 1, Balance: uint256.NewInt(0), Root: types.EmptyRootHash, CodeHash: types.EmptyCodeHash[:]},
		}},
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected collision error between programmatic alloc + spec alloc")
	}
}

// TestValidateRejectsSpecExceedingTargetSize pins the SPEC.md promise that
// Config.Validate fails when spec storage exceeds --target-size. Pre-fix
// this check was silently absent — users would get truncated state.
func TestValidateRejectsSpecExceedingTargetSize(t *testing.T) {
	addr := common.HexToAddress("0x000000000000000000000000000000000000dddd")
	// 1000 slots × 80 B/slot estimate = 80_000 bytes.
	bigStorage := make(map[common.Hash]common.Hash, 1000)
	for i := 0; i < 1000; i++ {
		var k common.Hash
		k[31] = byte(i)
		bigStorage[k] = common.HexToHash("0xaa")
	}

	cfg := &Config{
		TargetSize: 1000, // way below the 80_000 B estimate
		PreAlloc: []templates.PreAllocEntity{{
			Address: addr,
			Account: &types.StateAccount{Nonce: 1, Balance: uint256.NewInt(0), Root: types.EmptyRootHash, CodeHash: types.EmptyCodeHash[:]},
			Storage: storageMap(bigStorage),
		}},
	}
	err := cfg.Validate()
	if err == nil {
		t.Fatal("expected target-size violation, got nil")
	}
	if !strings.Contains(err.Error(), "--target-size") {
		t.Errorf("expected '--target-size' in error, got %q", err.Error())
	}
}

func TestValidateAcceptsSpecUnderTargetSize(t *testing.T) {
	addr := common.HexToAddress("0x000000000000000000000000000000000000eeee")
	smallStorage := map[common.Hash]common.Hash{
		common.HexToHash("0x01"): common.HexToHash("0xaa"),
	}
	cfg := &Config{
		TargetSize: 1_000_000, // way above 1 slot × 80 B
		PreAlloc: []templates.PreAllocEntity{{
			Address: addr,
			Account: &types.StateAccount{Nonce: 1, Balance: uint256.NewInt(0), Root: types.EmptyRootHash, CodeHash: types.EmptyCodeHash[:]},
			Storage: storageMap(smallStorage),
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Errorf("expected pass under budget, got %v", err)
	}
}

func TestPreAllocShimEmpty(t *testing.T) {
	// Empty Config — no PreAlloc, no legacy maps — must pass.
	cfg := &Config{}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// storageMap is a small test helper that wraps a map[common.Hash]common.Hash
// in the iter.Seq2 form PreAllocEntity.Storage expects. Mirrors the
// templates.MapToSeq helper to avoid an awkward cross-package dep.
func storageMap(m map[common.Hash]common.Hash) iter.Seq2[common.Hash, common.Hash] {
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
