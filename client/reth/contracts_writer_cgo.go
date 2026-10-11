//go:build cgo_reth

package reth

import (
	"bytes"
	"fmt"
	"sort"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"

	"github.com/ethereum/state-actor/generator"
	"github.com/ethereum/state-actor/internal/entitygen"
	iReth "github.com/ethereum/state-actor/internal/reth"
)

// WriteContracts writes the v2 contract tables: Bytecodes (deduped),
// AccountChangeSets (archive), HashedStorages, StorageChangeSets (archive).
// AccountsHistory + StoragesHistory rows (archive) are routed to the
// RocksDB CFs via envs.HistorySink().
//
// HashedAccounts is written by RunCgo's phase 2, not here (see
// hashedAccountValue in run_cgo.go).
//
// SIDE EFFECT: each contract's StateAccount.Root and .CodeHash are
// mutated in place from the supplied Storage + Code. With empty Storage
// the existing Root is preserved (the spec-storage streaming phase sets
// it ahead of time); a zero Root in that case is rejected.
//
// stats (optional) accumulates CodeBytes (deduped — only counts code
// that actually got written) and StorageBytes (sum of HashedStorages
// compact-encoded entries). Increments are applied only after the MDBX
// transaction commits.
func WriteContracts(envs *Envs, contracts []*entitygen.Account, blockNum uint64, archive bool, stats *generator.Stats) error {
	var (
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
				// Per-contract StoragesTrie emit: writes the intermediate-node
				// cache reth's payload builder reads during state-root
				// computation. DupSort main key = AddrHash; SubKey + BNC are
				// co-encoded into the value via StorageTrieEntry. Rows are
				// buffered and sorted bytewise before Put (HashBuilder's
				// post-order emission is not key-ascending) so libmdbx packs
				// the dup-sort subtree densely. Plain Put (not AppendDup) —
				// contracts in this batch aren't addrHash-sorted, so
				// AppendDup's monotonic-main-key precondition would fail on
				// cross-contract boundaries.
				var rows [][]byte
				emit := func(path iReth.StoredNibbles, node iReth.BranchNodeCompact) error {
					if path.Length == 0 {
						// Skip root branch — see TestNoRootCacheRows.
						return nil
					}
					var valBuf bytes.Buffer
					entry := iReth.StorageTrieEntry{SubKey: path, Node: node}
					// v2: 33-byte PackedStoredNibblesSubKey + BNC.
					entry.EncodePackedCompact(&valBuf)
					rows = append(rows, valBuf.Bytes())
					return nil
				}
				var err error
				storageRoot, err = computeStorageRoot(contract.Storage, emit)
				if err != nil {
					return fmt.Errorf("WriteContracts: computeStorageRoot %s: %w", contract.Address.Hex(), err)
				}
				sort.Slice(rows, func(i, j int) bool { return bytes.Compare(rows[i], rows[j]) < 0 })
				for _, row := range rows {
					// DupSort: main key = keccak(address); value = packed SubKey || BNC.
					if err := txn.Put(envs.MdbxDBIs["StoragesTrie"], contract.AddrHash[:], row, 0); err != nil {
						return fmt.Errorf("WriteContracts: StoragesTrie %s: %w", contract.Address.Hex(), err)
					}
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

			if archive {
				// AccountChangeSets: DupSort BE_u64(block) → AccountBeforeTx{addr, nil}
				abt := iReth.AccountBeforeTx{Address: contract.Address, Info: nil}
				var abtBuf bytes.Buffer
				abt.EncodeCompact(&abtBuf)
				if err := txn.Put(envs.MdbxDBIs["AccountChangeSets"], blockKey[:], abtBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("AccountChangeSets %s: %w", contract.Address.Hex(), err)
				}
				// AccountsHistory → RocksDB CF (v2 routing).
				if err := envs.HistorySink().PutAccountHistory(contract.Address, blockNum); err != nil {
					return fmt.Errorf("AccountsHistory %s: %w", contract.Address.Hex(), err)
				}
			}

			storBytes, err := WriteContractStorage(envs, txn, contract, blockNum, archive)
			if err != nil {
				return fmt.Errorf("WriteContracts: WriteContractStorage %s: %w", contract.Address.Hex(), err)
			}

			localStorageBytes += storBytes
		}
		return nil
	})
	if err != nil {
		return err
	}
	if stats != nil {
		stats.StorageBytes += localStorageBytes
		stats.CodeBytes += localCodeBytes
	}
	return nil
}
