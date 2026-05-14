package generator

import (
	"iter"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/internal/templates"
)

// TestPreAllocShimMaterializes verifies Validate() folds Config.PreAlloc
// account headers + bytecodes into the GenesisAccounts/Code maps so
// client writers can consume them uniformly. Storage is intentionally
// NOT drained — it stays on c.PreAlloc and is consumed lazily by each
// client's streaming spec-storage Phase via internal/streamingtrie
// (bounds RAM regardless of slot count).
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
	// Storage is NOT drained into GenesisStorage anymore; the iter stays
	// on c.PreAlloc for the per-client streaming Phase to consume.
	if got := cfg.GenesisStorage[addr]; len(got) != 0 {
		t.Errorf("shim should NOT drain storage; got %v entries", len(got))
	}

	// PreAlloc must be preserved (writers iterate it for streaming).
	if len(cfg.PreAlloc) != 1 {
		t.Errorf("Validate should preserve PreAlloc; got %d entries", len(cfg.PreAlloc))
	}
	// Second Validate must succeed (idempotent — same pointers, no
	// collision).
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

// TestValidateAcceptsSpecRegardlessOfStorageSize confirms the pre-
// Validate target-size estimate was removed when storage moved to
// streaming (internal/streamingtrie). The check is now enforced at
// write time by each per-client writer's dirSize sampling — Validate
// can't see slot counts without iterating the (potentially huge) lazy
// iter, which would defeat the bounded-RAM property.
func TestValidateAcceptsSpecRegardlessOfStorageSize(t *testing.T) {
	addr := common.HexToAddress("0x000000000000000000000000000000000000dddd")
	// 1000 slots — under the old 80 B/slot × 1000 = 80_000 B estimate.
	bigStorage := make(map[common.Hash]common.Hash, 1000)
	for i := 0; i < 1000; i++ {
		var k common.Hash
		k[31] = byte(i)
		bigStorage[k] = common.HexToHash("0xaa")
	}

	cfg := &Config{
		TargetSize: 1000, // way below what the old check would have rejected
		PreAlloc: []templates.PreAllocEntity{{
			Address: addr,
			Account: &types.StateAccount{Nonce: 1, Balance: uint256.NewInt(0), Root: types.EmptyRootHash, CodeHash: types.EmptyCodeHash[:]},
			Storage: storageMap(bigStorage),
		}},
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate should not reject on storage size (write-time cap handles it): %v", err)
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
	// Empty Config — no PreAlloc, no materialized maps — must pass.
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
