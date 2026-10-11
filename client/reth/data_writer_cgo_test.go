//go:build cgo_reth

package reth

import (
	"bytes"
	"math/rand"
	"testing"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/linxGnu/grocksdb"

	"github.com/ethereum/state-actor/internal/entitygen"
	iReth "github.com/ethereum/state-actor/internal/reth"
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

	if err := WriteEOAs(envs, accounts, 0, true /* archive */); err != nil {
		t.Fatalf("WriteEOAs: %v", err)
	}

	// v2 invariant: PlainAccountState MUST be empty.
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		cur, err := txn.OpenCursor(envs.MdbxDBIs["PlainAccountState"])
		if err != nil {
			return err
		}
		defer cur.Close()
		k, _, err := cur.Get(nil, nil, mdbx.First)
		if err == nil {
			t.Errorf("PlainAccountState should be empty under v2, first key = %x", k)
		}
		return nil
	}); err != nil {
		t.Errorf("v2 invariant check on PlainAccountState: %v", err)
	}

	// HashedAccounts is no longer written by WriteEOAs — RunCgo appends it
	// in addrHash order during phase 2 instead. Guard the no-write side effect.
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		cur, err := txn.OpenCursor(envs.MdbxDBIs["HashedAccounts"])
		if err != nil {
			return err
		}
		defer cur.Close()
		if k, _, err := cur.Get(nil, nil, mdbx.First); err == nil {
			t.Errorf("HashedAccounts should stay empty after WriteEOAs, first key = %x", k)
		}
		return nil
	}); err != nil {
		t.Errorf("HashedAccounts empty check: %v", err)
	}

	// Read back AccountsHistory from the RocksDB CF (v2 routing). Flush the
	// historySink first so the WAL-disabled batch lands in the memtable
	// where GetCF can see it.
	if err := envs.HistorySink().Flush(); err != nil {
		t.Fatalf("historySink.Flush: %v", err)
	}
	ro := grocksdb.NewDefaultReadOptions()
	defer ro.Destroy()
	for _, acc := range accounts {
		sk := iReth.ShardedKeyAddress{Address: acc.Address, BlockNumber: ^uint64(0)}
		var keyBuf bytes.Buffer
		sk.EncodeKey(&keyBuf)
		val, err := envs.RocksDB.GetCF(ro, envs.RocksCFs["AccountsHistory"], keyBuf.Bytes())
		if err != nil {
			t.Errorf("GetCF AccountsHistory %s: %v", acc.Address.Hex(), err)
			continue
		}
		list, _ := iReth.DecodeIntegerList(val.Data())
		if len(list) != 1 || list[0] != 0 {
			t.Errorf("AccountsHistory for %s: got %v, want [0]", acc.Address.Hex(), list)
		}
		val.Free()
	}
}

// TestWriteEOAsArchiveFalseIsNoop pins the new no-op contract: with
// HashedAccounts moved to RunCgo's phase-2 Append and no other per-EOA
// side effect left for archive=false, WriteEOAs must leave AccountChangeSets
// (and the RocksDB AccountsHistory CF) untouched when archive is off.
func TestWriteEOAsArchiveFalseIsNoop(t *testing.T) {
	tmp := t.TempDir()
	envs, err := OpenEnvs(tmp, true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()

	rng := rand.New(rand.NewSource(0xfeed))
	const n = 10
	accounts := make([]*entitygen.Account, n)
	for i := range n {
		accounts[i] = entitygen.GenerateEOA(rng)
	}

	if err := WriteEOAs(envs, accounts, 0, false /* archive */); err != nil {
		t.Fatalf("WriteEOAs: %v", err)
	}

	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		cur, err := txn.OpenCursor(envs.MdbxDBIs["AccountChangeSets"])
		if err != nil {
			return err
		}
		defer cur.Close()
		if k, _, err := cur.Get(nil, nil, mdbx.First); err == nil {
			t.Errorf("AccountChangeSets should stay empty with archive=false, first key = %x", k)
		}
		return nil
	}); err != nil {
		t.Errorf("AccountChangeSets empty check: %v", err)
	}
}
