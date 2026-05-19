//go:build cgo_reth

package reth

import (
	"bytes"
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/entitygen"
	iReth "github.com/nerolation/state-actor/internal/reth"
)

// WriteContracts writes the 9 reth tables for each contract: Bytecodes
// (deduped), PlainAccountState, HashedAccounts, AccountChangeSets,
// AccountsHistory, PlainStorageState, HashedStorages, StorageChangeSets,
// StoragesHistory.
//
// SIDE EFFECT: each contract's StateAccount.Root and .CodeHash are
// mutated in place from the supplied Storage + Code. With empty Storage
// the existing Root is preserved (the spec-storage streaming Phase sets
// it ahead of time); a zero Root in that case is rejected.
//
// stats (optional) accumulates AccountBytes, CodeBytes (deduped — only
// counts code that actually got written), and StorageBytes (sum of
// PlainStorageState compact-encoded entries). Increments are applied
// only after the MDBX transaction commits.
func WriteContracts(envs *Envs, contracts []*entitygen.Account, blockNum uint64, archive bool, stats *generator.Stats) error {
	var (
		localAccountBytes uint64
		localStorageBytes uint64
		localCodeBytes    uint64
	)
	err := envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		blockKey := beU64(blockNum)

		// Shared BytecodeWriter deduplicates across all contracts in this call.
		bw := NewBytecodeWriter(txn, envs.MdbxDBIs["Bytecodes"], 100_000)

		for _, contract := range contracts {
			if contract.StateAccount == nil {
				return fmt.Errorf("WriteContracts: contract %s has nil StateAccount", contract.Address.Hex())
			}

			var storageRoot common.Hash
			if len(contract.Storage) > 0 {
				var err error
				storageRoot, err = computeStorageRoot(contract.Storage)
				if err != nil {
					return fmt.Errorf("WriteContracts: computeStorageRoot %s: %w", contract.Address.Hex(), err)
				}
			} else {
				storageRoot = contract.StateAccount.Root
				if storageRoot == (common.Hash{}) {
					return fmt.Errorf("WriteContracts: contract %s has empty Storage AND zero StateAccount.Root — "+
						"caller must set Root (e.g. types.EmptyRootHash) before calling WriteContracts",
						contract.Address.Hex())
				}
			}

			codeHash, wrote, err := bw.Write(contract.Code)
			if err != nil {
				return fmt.Errorf("WriteContracts: bytecode write %s: %w", contract.Address.Hex(), err)
			}
			if wrote {
				localCodeBytes += uint64(len(contract.Code))
			}

			contract.StateAccount.Root = storageRoot
			contract.StateAccount.CodeHash = codeHash.Bytes()

			ethAccount := iReth.Account{
				Nonce:        contract.StateAccount.Nonce,
				Balance:      contract.StateAccount.Balance,
				BytecodeHash: &codeHash,
			}
			var accBuf bytes.Buffer
			ethAccount.EncodeCompact(&accBuf)
			accountBytes := accBuf.Bytes()

			// PlainAccountState: raw addr → Account
			if err := txn.Put(envs.MdbxDBIs["PlainAccountState"], contract.Address[:], accountBytes, 0); err != nil {
				return fmt.Errorf("PlainAccountState %s: %w", contract.Address.Hex(), err)
			}
			// HashedAccounts: keccak(addr) → Account
			if err := txn.Put(envs.MdbxDBIs["HashedAccounts"], contract.AddrHash[:], accountBytes, 0); err != nil {
				return fmt.Errorf("HashedAccounts %s: %w", contract.Address.Hex(), err)
			}
			if archive {
				// AccountChangeSets: DupSort BE_u64(block) → AccountBeforeTx{addr, nil}
				abt := iReth.AccountBeforeTx{Address: contract.Address, Info: nil}
				var abtBuf bytes.Buffer
				abt.EncodeCompact(&abtBuf)
				if err := txn.Put(envs.MdbxDBIs["AccountChangeSets"], blockKey[:], abtBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("AccountChangeSets %s: %w", contract.Address.Hex(), err)
				}
				// AccountsHistory: ShardedKey(addr, u64::MAX) → IntegerList([blockNum])
				shardedKey := iReth.ShardedKeyAddress{Address: contract.Address, BlockNumber: ^uint64(0)}
				var keyBuf bytes.Buffer
				shardedKey.EncodeKey(&keyBuf)
				var listBuf bytes.Buffer
				iReth.EncodeIntegerList(&listBuf, []uint64{blockNum})
				if err := txn.Put(envs.MdbxDBIs["AccountsHistory"], keyBuf.Bytes(), listBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("AccountsHistory %s: %w", contract.Address.Hex(), err)
				}
			}

			storBytes, err := WriteContractStorage(txn, envs.MdbxDBIs, contract, blockNum, archive)
			if err != nil {
				return fmt.Errorf("WriteContracts: WriteContractStorage %s: %w", contract.Address.Hex(), err)
			}

			localAccountBytes += uint64(len(accountBytes))
			localStorageBytes += storBytes
		}
		return nil
	})
	if err != nil {
		return err
	}
	if stats != nil {
		stats.AccountBytes += localAccountBytes
		stats.StorageBytes += localStorageBytes
		stats.CodeBytes += localCodeBytes
	}
	return nil
}
