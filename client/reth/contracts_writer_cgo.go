//go:build cgo_reth

package reth

import (
	"bytes"
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"

	"github.com/nerolation/state-actor/generator"
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
//
// stats (optional) accumulates AccountBytes (compact-Account encoding),
// CodeBytes (raw bytecode length, only counted when BytecodeWriter actually
// writes — duplicate code is skipped at the LRU/DB layer and does not
// inflate the count), and StorageBytes (sum of PlainStorageState compact-
// encoded entries — mirrors nethermind's value-bytes semantics). Pass nil
// to skip accounting. Increments are applied to stats only after the MDBX
// transaction commits; a write that rolls back leaves stats untouched.
func WriteContracts(envs *Envs, contracts []*entitygen.Account, blockNum uint64, stats *generator.Stats) error {
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

			// Step 1: compute per-contract storage root.
			storageRoot, err := computeStorageRoot(contract.Storage)
			if err != nil {
				return fmt.Errorf("WriteContracts: computeStorageRoot %s: %w", contract.Address.Hex(), err)
			}

			// Step 2: write bytecode and get the code hash. `wrote` is false on
			// dedup hits (LRU or DB) — gate the byte count so duplicate code
			// doesn't inflate stats.CodeBytes beyond what's persisted.
			codeHash, wrote, err := bw.Write(contract.Code)
			if err != nil {
				return fmt.Errorf("WriteContracts: bytecode write %s: %w", contract.Address.Hex(), err)
			}
			if wrote {
				localCodeBytes += uint64(len(contract.Code))
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

			// Step 5: write the 4 storage tables via WriteContractStorage. The
			// returned uint64 is the sum of PlainStorageState entry sizes — see
			// WriteContractStorage's docstring for the byte semantics.
			storBytes, err := WriteContractStorage(txn, envs.MdbxDBIs, contract, blockNum)
			if err != nil {
				return fmt.Errorf("WriteContracts: WriteContractStorage %s: %w", contract.Address.Hex(), err)
			}

			// All Puts for this contract succeeded — bank into the local
			// accumulators. Transferred to the user's stats only if the
			// enclosing Update commits.
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

