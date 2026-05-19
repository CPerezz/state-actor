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
		_, err := WriteContractStorage(txn, envs.MdbxDBIs, contract, 0, true /* archive */)
		return err
	})
	if err != nil {
		t.Fatalf("WriteContractStorage: %v", err)
	}

	// Verify PlainStorageState — count entries under contract.Address.
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		cur, err := txn.OpenCursor(envs.MdbxDBIs["PlainStorageState"])
		if err != nil {
			return err
		}
		defer cur.Close()
		count := 0
		for k, _, err := cur.Get(addr[:], nil, mdbx.SetKey); err == nil; k, _, err = cur.Get(nil, nil, mdbx.NextDup) {
			if !bytes.Equal(k, addr[:]) {
				break
			}
			count++
		}
		if count != len(contract.Storage) {
			t.Errorf("PlainStorageState: %d entries for %s, want %d", count, addr.Hex(), len(contract.Storage))
		}
		return nil
	}); err != nil {
		t.Errorf("verify PlainStorageState: %v", err)
	}

	// Verify HashedStorages similarly under addrHash.
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

	// Spot-check StoragesHistory exists for slot 0x01.
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		var keyBuf bytes.Buffer
		ssk := iReth.StorageShardedKey{
			Address:     addr,
			StorageKey:  common.HexToHash("0x01"),
			BlockNumber: ^uint64(0),
		}
		ssk.EncodeKey(&keyBuf)
		val, err := txn.Get(envs.MdbxDBIs["StoragesHistory"], keyBuf.Bytes())
		if err != nil {
			return err
		}
		list, _ := iReth.DecodeIntegerList(val)
		if len(list) != 1 || list[0] != 0 {
			t.Errorf("StoragesHistory: list = %v, want [0]", list)
		}
		return nil
	}); err != nil {
		t.Errorf("verify StoragesHistory: %v", err)
	}
}

// TestWriteContractStorage_FullMode: with archive=false (full mode, the
// default), WriteContractStorage must populate PlainStorageState and
// HashedStorages but elide BOTH archive-only tables (StorageChangeSets
// and StoragesHistory).
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
		_, err := WriteContractStorage(txn, envs.MdbxDBIs, contract, 0, false /* archive */)
		return err
	})
	if err != nil {
		t.Fatalf("WriteContractStorage(archive=false): %v", err)
	}

	countRows := func(t *testing.T, table string) int {
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

	if got := countRows(t, "StorageChangeSets"); got != 0 {
		t.Errorf("StorageChangeSets rows = %d, want 0 (full mode skips)", got)
	}
	if got := countRows(t, "StoragesHistory"); got != 0 {
		t.Errorf("StoragesHistory rows = %d, want 0 (full mode skips)", got)
	}
	if got := countRows(t, "PlainStorageState"); got != len(contract.Storage) {
		t.Errorf("PlainStorageState rows = %d, want %d", got, len(contract.Storage))
	}
	if got := countRows(t, "HashedStorages"); got != len(contract.Storage) {
		t.Errorf("HashedStorages rows = %d, want %d", got, len(contract.Storage))
	}
}

// TestWriteContractStorage_Archive: with archive=true, all four tables
// populate at the expected counts. Mirror of _FullMode for the opt-in
// archive-mode path.
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
		_, err := WriteContractStorage(txn, envs.MdbxDBIs, contract, 0, true /* archive */)
		return err
	})
	if err != nil {
		t.Fatalf("WriteContractStorage(archive=true): %v", err)
	}

	countRows := func(t *testing.T, table string) int {
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

	for _, table := range []string{"PlainStorageState", "HashedStorages", "StorageChangeSets", "StoragesHistory"} {
		if got := countRows(t, table); got != len(contract.Storage) {
			t.Errorf("%s rows = %d, want %d", table, got, len(contract.Storage))
		}
	}
}
