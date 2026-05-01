//go:build cgo_reth

package reth

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/crypto"

	"github.com/nerolation/state-actor/internal/entitygen"
	iReth "github.com/nerolation/state-actor/internal/reth"
)

// WriteContracts writes all data tables for a slice of contract accounts:
// Bytecodes (deduped), PlainAccountState, HashedAccounts, AccountChangeSets,
// AccountsHistory, PlainStorageState, HashedStorages, StorageChangeSets,
// StoragesHistory.
//
// SIDE EFFECT: each contract's StateAccount is mutated to have:
//   - StateAccount.Root = storage root (computed from contract.Storage)
//   - StateAccount.CodeHash = bytecode hash (computed from contract.Code)
//
// This makes ComputeStateRoot work correctly afterward — it RLP-encodes
// StateAccount as-is, so Root/CodeHash must already be populated.
//
// blockNum is the block at which these contracts came into existence
// (0 for genesis).
func WriteContracts(envs *Envs, contracts []*entitygen.Account, blockNum uint64) error {
	return envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		blockKey := beU64(blockNum)

		// Shared BytecodeWriter deduplicates across all contracts in this call.
		bw := NewBytecodeWriter(txn, envs.MdbxDBIs["Bytecodes"], 100_000)

		for _, contract := range contracts {
			if contract.StateAccount == nil {
				return fmt.Errorf("WriteContracts: contract %s has nil StateAccount", contract.Address.Hex())
			}

			// Step 1: compute per-contract storage root.
			storageRoot, err := computeStorageRoot(contract.Storage)
			if err != nil {
				return fmt.Errorf("WriteContracts: computeStorageRoot %s: %w", contract.Address.Hex(), err)
			}

			// Step 2: write bytecode and get the code hash.
			codeHash, err := bw.Write(contract.Code)
			if err != nil {
				return fmt.Errorf("WriteContracts: bytecode write %s: %w", contract.Address.Hex(), err)
			}

			// Step 3: splice storage root and code hash into StateAccount.
			contract.StateAccount.Root = storageRoot
			contract.StateAccount.CodeHash = codeHash.Bytes()

			// Step 4: encode and write the 4 account-state tables.
			ethAccount := iReth.Account{
				Nonce:        contract.StateAccount.Nonce,
				Balance:      contract.StateAccount.Balance,
				BytecodeHash: &codeHash,
			}
			var accBuf bytes.Buffer
			ethAccount.EncodeCompact(&accBuf)
			accountBytes := accBuf.Bytes()

			// PlainAccountState — raw addr → Account
			if err := txn.Put(envs.MdbxDBIs["PlainAccountState"], contract.Address[:], accountBytes, 0); err != nil {
				return fmt.Errorf("PlainAccountState %s: %w", contract.Address.Hex(), err)
			}

			// HashedAccounts — keccak(addr) → Account
			if err := txn.Put(envs.MdbxDBIs["HashedAccounts"], contract.AddrHash[:], accountBytes, 0); err != nil {
				return fmt.Errorf("HashedAccounts %s: %w", contract.Address.Hex(), err)
			}

			// AccountChangeSets — DupSort: BE_u64(block) → AccountBeforeTx{addr, nil}
			abt := iReth.AccountBeforeTx{Address: contract.Address, Info: nil}
			var abtBuf bytes.Buffer
			abt.EncodeCompact(&abtBuf)
			if err := txn.Put(envs.MdbxDBIs["AccountChangeSets"], blockKey[:], abtBuf.Bytes(), 0); err != nil {
				return fmt.Errorf("AccountChangeSets %s: %w", contract.Address.Hex(), err)
			}

			// AccountsHistory — ShardedKey(addr, u64::MAX) → IntegerList([blockNum])
			shardedKey := iReth.ShardedKeyAddress{Address: contract.Address, BlockNumber: ^uint64(0)}
			var keyBuf bytes.Buffer
			shardedKey.EncodeKey(&keyBuf)
			var listBuf bytes.Buffer
			iReth.EncodeIntegerList(&listBuf, []uint64{blockNum})
			if err := txn.Put(envs.MdbxDBIs["AccountsHistory"], keyBuf.Bytes(), listBuf.Bytes(), 0); err != nil {
				return fmt.Errorf("AccountsHistory %s: %w", contract.Address.Hex(), err)
			}

			// Step 5: write the 4 storage tables via WriteContractStorage.
			if err := WriteContractStorage(txn, envs.MdbxDBIs, contract, blockNum); err != nil {
				return fmt.Errorf("WriteContracts: WriteContractStorage %s: %w", contract.Address.Hex(), err)
			}
		}
		return nil
	})
}

// computeStorageRoot computes the MPT root over the contract's storage slots.
//
// Leaves are keccak(slot_key)-sorted; each value is RLP-encoded with
// leading-zero stripping (Ethereum convention). Empty storage returns the
// canonical empty-MPT root 0x56e81f17...
func computeStorageRoot(slots []entitygen.StorageSlot) (common.Hash, error) {
	if len(slots) == 0 {
		// Canonical empty storage trie root: keccak256(rlp([])).
		return common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"), nil
	}

	// Sort by keccak(slot_key) ascending.
	type hashedSlot struct {
		keyHash common.Hash
		value   common.Hash
	}
	sorted := make([]hashedSlot, len(slots))
	for i, s := range slots {
		sorted[i] = hashedSlot{
			keyHash: crypto.Keccak256Hash(s.Key[:]),
			value:   s.Value,
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].keyHash[:], sorted[j].keyHash[:]) < 0
	})

	hb := iReth.NewHashBuilder(func(_ iReth.StoredNibbles, _ iReth.BranchNodeCompact) error {
		return nil // storage trie nodes aren't persisted here
	})

	for _, s := range sorted {
		// RLP-encode the value bytes with leading zeros stripped.
		valBytes := s.value[:]
		for len(valBytes) > 0 && valBytes[0] == 0 {
			valBytes = valBytes[1:]
		}
		valRLP, err := rlp.EncodeToBytes(valBytes)
		if err != nil {
			return common.Hash{}, fmt.Errorf("rlp encode slot value: %w", err)
		}
		// Reuse addrHashToNibbles — same nibble expansion logic for any 32-byte key.
		if err := hb.AddLeaf(addrHashToNibbles(s.keyHash[:]), valRLP); err != nil {
			return common.Hash{}, fmt.Errorf("AddLeaf: %w", err)
		}
	}
	return hb.Root(), nil
}
