//go:build cgo_reth

package reth

import (
	"bytes"
	"testing"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
	"github.com/linxGnu/grocksdb"

	"github.com/nerolation/state-actor/internal/entitygen"
	iReth "github.com/nerolation/state-actor/internal/reth"
)

func TestWriteContractStorageRoundtrip(t *testing.T) {
	tmp := t.TempDir()
	envs, err := OpenEnvs(tmp, true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()

	addr := common.HexToAddress("0xdeadbeef")
	addrHash := crypto.Keccak256Hash(addr[:])
	contract := &entitygen.Account{
		Address:  addr,
		AddrHash: addrHash,
		StateAccount: &types.StateAccount{
			Nonce:   1,
			Balance: uint256.NewInt(0),
		},
		Storage: []entitygen.StorageSlot{
			{Key: common.HexToHash("0x01"), Value: common.HexToHash("0xa")},
			{Key: common.HexToHash("0x02"), Value: common.HexToHash("0xb")},
			{Key: common.HexToHash("0x03"), Value: common.HexToHash("0xc")},
		},
	}

	err = envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		_, err := WriteContractStorage(envs, txn, contract, 0, true /* archive */)
		return err
	})
	if err != nil {
		t.Fatalf("WriteContractStorage: %v", err)
	}

	// v2 invariant: PlainStorageState must be empty (HashedStorages is the
	// canonical state). Regression catcher for "future commit reintroduces
	// a PlainStorageState write".
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		cur, err := txn.OpenCursor(envs.MdbxDBIs["PlainStorageState"])
		if err != nil {
			return err
		}
		defer cur.Close()
		_, _, err = cur.Get(nil, nil, mdbx.First)
		if err == nil {
			t.Errorf("PlainStorageState non-empty on v2 datadir — Plain* writes have been re-introduced")
		}
		return nil
	}); err != nil {
		t.Errorf("verify PlainStorageState empty: %v", err)
	}

	// Verify HashedStorages — count entries under addrHash.
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		cur, err := txn.OpenCursor(envs.MdbxDBIs["HashedStorages"])
		if err != nil {
			return err
		}
		defer cur.Close()
		count := 0
		for k, _, err := cur.Get(addrHash[:], nil, mdbx.SetKey); err == nil; k, _, err = cur.Get(nil, nil, mdbx.NextDup) {
			if !bytes.Equal(k, addrHash[:]) {
				break
			}
			count++
		}
		if count != len(contract.Storage) {
			t.Errorf("HashedStorages: %d entries, want %d", count, len(contract.Storage))
		}
		return nil
	}); err != nil {
		t.Errorf("verify HashedStorages: %v", err)
	}

	// Spot-check StoragesHistory in the RocksDB CF (v2 routing).
	if err := envs.historySink.Flush(); err != nil {
		t.Fatalf("historySink.Flush: %v", err)
	}
	ro := grocksdb.NewDefaultReadOptions()
	defer ro.Destroy()
	var keyBuf bytes.Buffer
	ssk := iReth.StorageShardedKey{
		Address:     addr,
		StorageKey:  common.HexToHash("0x01"),
		BlockNumber: ^uint64(0),
	}
	ssk.EncodeKey(&keyBuf)
	val, err := envs.RocksDB.GetCF(ro, envs.RocksCFs["StoragesHistory"], keyBuf.Bytes())
	if err != nil {
		t.Fatalf("RocksDB StoragesHistory: %v", err)
	}
	list, _ := iReth.DecodeIntegerList(val.Data())
	val.Free()
	if len(list) != 1 || list[0] != 0 {
		t.Errorf("StoragesHistory: list = %v, want [0]", list)
	}
}

// countRows iterates an MDBX table cursor and returns the row count.
func countMDBXRows(t *testing.T, envs *Envs, table string) int {
	t.Helper()
	var count int
	_ = envs.Mdbx.View(func(txn *mdbx.Txn) error {
		cur, err := txn.OpenCursor(envs.MdbxDBIs[table])
		if err != nil {
			return err
		}
		defer cur.Close()
		for _, _, err := cur.Get(nil, nil, mdbx.First); err == nil; _, _, err = cur.Get(nil, nil, mdbx.Next) {
			count++
		}
		return nil
	})
	return count
}

// TestWriteContractStorage_FullMode: with archive=false (the default),
// WriteContractStorage populates HashedStorages only — archive-only tables
// (StorageChangeSets MDBX + StoragesHistory RocksDB CF) stay empty; the v2
// invariant tables (Plain*) also stay empty.
func TestWriteContractStorage_FullMode(t *testing.T) {
	envs, err := OpenEnvs(t.TempDir(), true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()

	addr := common.HexToAddress("0xcafef00d")
	contract := &entitygen.Account{
		Address:  addr,
		AddrHash: crypto.Keccak256Hash(addr[:]),
		StateAccount: &types.StateAccount{
			Nonce:   1,
			Balance: uint256.NewInt(0),
		},
		Storage: []entitygen.StorageSlot{
			{Key: common.HexToHash("0x01"), Value: common.HexToHash("0xa")},
			{Key: common.HexToHash("0x02"), Value: common.HexToHash("0xb")},
		},
	}

	err = envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		_, err := WriteContractStorage(envs, txn, contract, 0, false /* archive */)
		return err
	})
	if err != nil {
		t.Fatalf("WriteContractStorage(archive=false): %v", err)
	}

	if got := countMDBXRows(t, envs, "StorageChangeSets"); got != 0 {
		t.Errorf("StorageChangeSets rows = %d, want 0 (full mode skips)", got)
	}
	if got := countMDBXRows(t, envs, "PlainStorageState"); got != 0 {
		t.Errorf("PlainStorageState rows = %d, want 0 on v2", got)
	}
	if got := countMDBXRows(t, envs, "HashedStorages"); got != len(contract.Storage) {
		t.Errorf("HashedStorages rows = %d, want %d", got, len(contract.Storage))
	}
}

// TestWriteContractStorage_Archive: with archive=true, HashedStorages +
// StorageChangeSets populate at the expected counts AND the RocksDB
// StoragesHistory CF receives one entry per slot (v2 routing).
func TestWriteContractStorage_Archive(t *testing.T) {
	envs, err := OpenEnvs(t.TempDir(), true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()

	addr := common.HexToAddress("0xcafef00d")
	contract := &entitygen.Account{
		Address:  addr,
		AddrHash: crypto.Keccak256Hash(addr[:]),
		StateAccount: &types.StateAccount{
			Nonce:   1,
			Balance: uint256.NewInt(0),
		},
		Storage: []entitygen.StorageSlot{
			{Key: common.HexToHash("0x01"), Value: common.HexToHash("0xa")},
			{Key: common.HexToHash("0x02"), Value: common.HexToHash("0xb")},
		},
	}

	err = envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		_, err := WriteContractStorage(envs, txn, contract, 0, true /* archive */)
		return err
	})
	if err != nil {
		t.Fatalf("WriteContractStorage(archive=true): %v", err)
	}
	if err := envs.historySink.Flush(); err != nil {
		t.Fatalf("historySink.Flush: %v", err)
	}

	if got := countMDBXRows(t, envs, "PlainStorageState"); got != 0 {
		t.Errorf("PlainStorageState rows = %d, want 0 on v2", got)
	}
	for _, table := range []string{"HashedStorages", "StorageChangeSets"} {
		if got := countMDBXRows(t, envs, table); got != len(contract.Storage) {
			t.Errorf("%s rows = %d, want %d", table, got, len(contract.Storage))
		}
	}
	// MDBX StoragesHistory must stay empty on v2 (history lives in RocksDB).
	if got := countMDBXRows(t, envs, "StoragesHistory"); got != 0 {
		t.Errorf("MDBX StoragesHistory rows = %d, want 0 (v2 routes history to RocksDB)", got)
	}
	// Verify the RocksDB CF received one row per slot.
	ro := grocksdb.NewDefaultReadOptions()
	defer ro.Destroy()
	for _, slot := range contract.Storage {
		ssk := iReth.StorageShardedKey{
			Address:     addr,
			StorageKey:  slot.Key,
			BlockNumber: ^uint64(0),
		}
		var keyBuf bytes.Buffer
		ssk.EncodeKey(&keyBuf)
		val, err := envs.RocksDB.GetCF(ro, envs.RocksCFs["StoragesHistory"], keyBuf.Bytes())
		if err != nil {
			t.Errorf("RocksDB StoragesHistory %s: %v", slot.Key.Hex(), err)
			continue
		}
		if val.Size() == 0 {
			t.Errorf("RocksDB StoragesHistory missing slot %s", slot.Key.Hex())
		}
		val.Free()
	}
}
