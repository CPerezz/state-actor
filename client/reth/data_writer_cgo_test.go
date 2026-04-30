//go:build cgo_reth

package reth

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/erigontech/mdbx-go/mdbx"

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

	if err := WriteEOAs(envs, accounts, 0); err != nil {
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
