//go:build cgo_reth

package reth

import (
	"bytes"
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/internal/entitygen"
	iReth "github.com/nerolation/state-actor/internal/reth"
)

// WriteContractStorage writes per-slot rows across the four storage tables
// for a single contract. accounts must be a contract account (StateAccount
// + Storage slots populated). blockNum is the block at which storage was
// newly written (0 for genesis).
//
// All slots written within ONE MDBX transaction (the caller's). Per-slot
// writes are 4 rows:
//   - PlainStorageState (DupSort): Address → StorageEntry{slot_key, slot_value}
//   - HashedStorages (DupSort): keccak(Address) → StorageEntry{keccak(slot_key), slot_value}
//   - StorageChangeSets (DupSort): BlockNumberAddress → StorageEntry{slot_key, prev=0}
//   - StoragesHistory: StorageShardedKey{addr, slot_key, u64::MAX} → IntegerList([block])
//
// For genesis (blockNum=0) the "before" value in StorageChangeSets is 0 —
// the slot was newly set. Composable with WriteEOAs / future WriteContracts.
func WriteContractStorage(
	txn *mdbx.Txn,
	dbis map[string]mdbx.DBI,
	contract *entitygen.Account,
	blockNum uint64,
) error {
	blockKey := iReth.BlockNumberAddress{BlockNumber: blockNum, Address: contract.Address}
	var blockKeyBuf bytes.Buffer
	blockKey.EncodeKey(&blockKeyBuf)
	blockKeyBytes := blockKeyBuf.Bytes()

	for _, slot := range contract.Storage {
		slotValueU256 := uint256.NewInt(0).SetBytes(slot.Value[:])

		// 1. PlainStorageState: Address → StorageEntry{slot_key, slot_value}
		plainEntry := iReth.StorageEntry{Key: slot.Key, Value: slotValueU256}
		var plainBuf bytes.Buffer
		plainEntry.EncodeCompact(&plainBuf)
		if err := txn.Put(dbis["PlainStorageState"], contract.Address[:], plainBuf.Bytes(), 0); err != nil {
			return fmt.Errorf("PlainStorageState %s slot %s: %w",
				contract.Address.Hex(), slot.Key.Hex(), err)
		}

		// 2. HashedStorages: keccak(Address) → StorageEntry{keccak(slot_key), slot_value}
		hashedSlotKey := crypto.Keccak256Hash(slot.Key[:])
		hashedEntry := iReth.StorageEntry{Key: hashedSlotKey, Value: slotValueU256}
		var hashedBuf bytes.Buffer
		hashedEntry.EncodeCompact(&hashedBuf)
		if err := txn.Put(dbis["HashedStorages"], contract.AddrHash[:], hashedBuf.Bytes(), 0); err != nil {
			return fmt.Errorf("HashedStorages %s slot %s: %w",
				contract.AddrHash.Hex(), slot.Key.Hex(), err)
		}

		// 3. StorageChangeSets: BlockNumberAddress → StorageEntry{slot_key, prev_value=0}
		// For genesis (block 0), the "before" value is 0 — slot was newly set.
		changeEntry := iReth.StorageEntry{Key: slot.Key, Value: uint256.NewInt(0)}
		var changeBuf bytes.Buffer
		changeEntry.EncodeCompact(&changeBuf)
		if err := txn.Put(dbis["StorageChangeSets"], blockKeyBytes, changeBuf.Bytes(), 0); err != nil {
			return fmt.Errorf("StorageChangeSets %s slot %s: %w",
				contract.Address.Hex(), slot.Key.Hex(), err)
		}

		// 4. StoragesHistory: StorageShardedKey{addr, slot_key, u64::MAX} → IntegerList([block])
		// u64::MAX marks the latest (open) shard; the bitmap contains the block
		// numbers at which this slot was first touched.
		ssk := iReth.StorageShardedKey{
			Address:     contract.Address,
			StorageKey:  slot.Key,
			BlockNumber: ^uint64(0),
		}
		var sskBuf bytes.Buffer
		ssk.EncodeKey(&sskBuf)
		var listBuf bytes.Buffer
		iReth.EncodeIntegerList(&listBuf, []uint64{blockNum})
		if err := txn.Put(dbis["StoragesHistory"], sskBuf.Bytes(), listBuf.Bytes(), 0); err != nil {
			return fmt.Errorf("StoragesHistory %s slot %s: %w",
				contract.Address.Hex(), slot.Key.Hex(), err)
		}
	}
	return nil
}
