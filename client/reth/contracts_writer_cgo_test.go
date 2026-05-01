//go:build cgo_reth

package reth

import (
	"math/rand"
	"testing"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"

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

	if err := WriteContracts(envs, contracts, 0); err != nil {
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
