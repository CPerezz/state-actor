//go:build cgo_reth

package reth

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/erigontech/mdbx-go/mdbx"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/entitygen"
	iReth "github.com/nerolation/state-actor/internal/reth"
)

func TestWriteEOAsRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	envs, err := OpenEnvs(tmp, true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()

	rng := rand.New(rand.NewSource(0xc0ffee))
	const n = 10
	accounts := make([]*entitygen.Account, n)
	for i := 0; i < n; i++ {
		accounts[i] = entitygen.GenerateEOA(rng)
	}

	if err := WriteEOAs(envs, accounts, 0, nil); err != nil {
		t.Fatalf("WriteEOAs: %v", err)
	}

	// Read back PlainAccountState for each account; verify nonce + balance.
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		for _, acc := range accounts {
			val, err := txn.Get(envs.MdbxDBIs["PlainAccountState"], acc.Address[:])
			if err != nil {
				return err
			}
			var got iReth.Account
			got.DecodeCompact(val, len(val))
			if got.Nonce != acc.StateAccount.Nonce {
				t.Errorf("Nonce mismatch for %s: got %d want %d", acc.Address.Hex(), got.Nonce, acc.StateAccount.Nonce)
			}
			if !got.Balance.Eq(acc.StateAccount.Balance) {
				t.Errorf("Balance mismatch for %s: got %s want %s", acc.Address.Hex(), got.Balance, acc.StateAccount.Balance)
			}
			if got.BytecodeHash != nil {
				t.Errorf("BytecodeHash should be nil for EOA %s", acc.Address.Hex())
			}
		}
		return nil
	}); err != nil {
		t.Errorf("read-back PlainAccountState: %v", err)
	}

	// Read back HashedAccounts — entry must exist for each account.
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		for _, acc := range accounts {
			val, err := txn.Get(envs.MdbxDBIs["HashedAccounts"], acc.AddrHash[:])
			if err != nil {
				return err
			}
			if len(val) == 0 {
				t.Errorf("HashedAccounts empty for %s", acc.AddrHash.Hex())
			}
		}
		return nil
	}); err != nil {
		t.Errorf("read-back HashedAccounts: %v", err)
	}

	// Read back AccountsHistory — ShardedKey(addr, u64::MAX) must exist.
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		for _, acc := range accounts {
			sk := iReth.ShardedKeyAddress{Address: acc.Address, BlockNumber: ^uint64(0)}
			var keyBuf bytes.Buffer
			sk.EncodeKey(&keyBuf)
			val, err := txn.Get(envs.MdbxDBIs["AccountsHistory"], keyBuf.Bytes())
			if err != nil {
				return err
			}
			list, _ := iReth.DecodeIntegerList(val)
			if len(list) != 1 || list[0] != 0 {
				t.Errorf("AccountsHistory for %s: got %v, want [0]", acc.Address.Hex(), list)
			}
		}
		return nil
	}); err != nil {
		t.Errorf("read-back AccountsHistory: %v", err)
	}
}

// TestWriteEOAsPopulatesStats guards the silent-zero regression class from
// issue #70: any code path that drops the stats.AccountBytes increment must
// fail this test, regardless of whether the writer's other side-effects
// still work. This is the in-tree unit-level companion to
// main_test.go:TestMainBenchmarkPrintsStats (which only exercises geth).
func TestWriteEOAsPopulatesStats(t *testing.T) {
	tmp := t.TempDir()
	envs, err := OpenEnvs(tmp, true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()

	rng := rand.New(rand.NewSource(0xfeed))
	const n = 10
	accounts := make([]*entitygen.Account, n)
	for i := 0; i < n; i++ {
		accounts[i] = entitygen.GenerateEOA(rng)
	}

	var stats generator.Stats
	if err := WriteEOAs(envs, accounts, 0, &stats); err != nil {
		t.Fatalf("WriteEOAs: %v", err)
	}

	if stats.AccountBytes == 0 {
		t.Errorf("stats.AccountBytes == 0 after writing %d EOAs — accounting silently broken", n)
	}
	// Sanity: at minimum one compact-Account encoding per EOA. The compact
	// encoding for a non-zero-balance EOA is at least a few bytes; assert a
	// floor that's clearly above noise without being brittle.
	if got, min := stats.AccountBytes, uint64(n); got < min {
		t.Errorf("stats.AccountBytes = %d, want >= %d (one byte per account at minimum)", got, min)
	}
}
