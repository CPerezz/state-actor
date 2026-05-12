//go:build cgo_reth

package reth

import (
	"math/rand"
	"testing"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/entitygen"
)

func TestWriteContractsSmall(t *testing.T) {
	tmp := t.TempDir()
	envs, err := OpenEnvs(tmp, true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()

	rng := rand.New(rand.NewSource(0xdeadbeef))
	const n = 5
	contracts := make([]*entitygen.Account, n)
	for i := 0; i < n; i++ {
		// GenerateContract(rng, codeSize, numSlots): 16-byte minimum code, 3 storage slots.
		contracts[i] = entitygen.GenerateContract(rng, 16, 3)
	}

	if err := WriteContracts(envs, contracts, 0, nil); err != nil {
		t.Fatalf("WriteContracts: %v", err)
	}

	// Spot-check Bytecodes table has at most n entries (deduped).
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		cur, err := txn.OpenCursor(envs.MdbxDBIs["Bytecodes"])
		if err != nil {
			return err
		}
		defer cur.Close()
		count := 0
		for _, _, err := cur.Get(nil, nil, mdbx.First); err == nil; _, _, err = cur.Get(nil, nil, mdbx.Next) {
			count++
		}
		if count == 0 || count > n {
			t.Errorf("Bytecodes count = %d, expected 1..%d", count, n)
		}
		return nil
	}); err != nil {
		t.Errorf("verify Bytecodes: %v", err)
	}

	// Verify each contract's StateAccount.Root and CodeHash are now set.
	for _, c := range contracts {
		if c.StateAccount.Root == (common.Hash{}) {
			t.Errorf("contract %s: StateAccount.Root not set", c.Address.Hex())
		}
		if len(c.StateAccount.CodeHash) == 0 {
			t.Errorf("contract %s: StateAccount.CodeHash not set", c.Address.Hex())
		}
	}
}

// TestWriteContractsPopulatesStats is the per-writer companion to
// main_test.go:TestMainBenchmarkPrintsStats — it pins the byte accounting
// for the contracts path (Account + Storage + Code) so a future refactor
// dropping any of the three increments fails fast at unit-test level
// instead of silently in --benchmark output. See issue #70.
func TestWriteContractsPopulatesStats(t *testing.T) {
	tmp := t.TempDir()
	envs, err := OpenEnvs(tmp, true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()

	rng := rand.New(rand.NewSource(0xc0de))
	const n = 5
	contracts := make([]*entitygen.Account, n)
	for i := 0; i < n; i++ {
		// 32-byte code, 3 storage slots — large enough to make every byte
		// counter strictly positive.
		contracts[i] = entitygen.GenerateContract(rng, 32, 3)
	}

	var stats generator.Stats
	if err := WriteContracts(envs, contracts, 0, &stats); err != nil {
		t.Fatalf("WriteContracts: %v", err)
	}

	if stats.AccountBytes == 0 {
		t.Errorf("stats.AccountBytes == 0 after writing %d contracts", n)
	}
	if stats.StorageBytes == 0 {
		t.Errorf("stats.StorageBytes == 0 after writing %d contracts with storage", n)
	}
	if stats.CodeBytes == 0 {
		t.Errorf("stats.CodeBytes == 0 after writing %d contracts with code", n)
	}
}

// TestWriteContractsDedupesCodeBytes pins the post-dedup CodeBytes
// invariant: BytecodeWriter's LRU/DB dedup must not double-count code that
// wasn't actually written. Writing the same code twice in one batch should
// count the bytes once. Regression guard for the pre-fix behavior where
// stats.CodeBytes incremented unconditionally per contract.
func TestWriteContractsDedupesCodeBytes(t *testing.T) {
	tmp := t.TempDir()
	envs, err := OpenEnvs(tmp, true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()

	rng := rand.New(rand.NewSource(0xdedbeef))
	a := entitygen.GenerateContract(rng, 32, 1)
	b := entitygen.GenerateContract(rng, 32, 1)
	// Force b to use a's code so the BytecodeWriter dedup path triggers.
	b.Code = a.Code
	b.CodeHash = a.CodeHash

	var stats generator.Stats
	if err := WriteContracts(envs, []*entitygen.Account{a, b}, 0, &stats); err != nil {
		t.Fatalf("WriteContracts: %v", err)
	}

	wantCodeBytes := uint64(len(a.Code))
	if stats.CodeBytes != wantCodeBytes {
		t.Errorf("stats.CodeBytes = %d, want %d (duplicate code must dedupe and count once)",
			stats.CodeBytes, wantCodeBytes)
	}
}
