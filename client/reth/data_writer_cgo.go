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

// WriteEOAs writes data-table rows for each account: PlainAccountState,
// HashedAccounts, and (in archive mode) AccountChangeSets at blockNum
// + AccountsHistory.
//
// All writes happen in ONE MDBX write transaction. Atomic; on error no
// partial state is committed.
//
// Tables written per EOA:
//   - PlainAccountState (Address → Account) — always
//   - HashedAccounts (keccak(Address) → Account) — always
//   - AccountChangeSets (DupSort: BE_u64(block) → AccountBeforeTx{addr, nil}) — archive only
//   - AccountsHistory (ShardedKey(addr, u64::MAX) → IntegerList([blockNum])) — archive only
//
// In full mode (archive=false), the two archive-only tables are skipped:
// at block 0 there's no history to preserve, and a full-mode reth node
// prunes them once past the pruning window.
//
// Accounts are written in input order (caller is responsible for ordering).
// Uses tx.Put (not cursor.Append) for safety regardless of input ordering.
//
// stats (optional) accumulates AccountBytes — the encoded compact-Account
// size for every account written. Pass nil to skip accounting. The
// accumulator is applied to stats only after the MDBX transaction commits;
// a write that rolls back leaves stats untouched.
func WriteEOAs(envs *Envs, accounts []*entitygen.Account, blockNum uint64, archive bool, stats *generator.Stats) error {
	var localAccountBytes uint64
	err := envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		blockKey := beU64(blockNum)

		for _, acc := range accounts {
			if acc.StateAccount == nil {
				return fmt.Errorf("WriteEOAs: account %s has nil StateAccount", acc.Address.Hex())
			}

			ethAccount := iReth.Account{
				Nonce:        acc.StateAccount.Nonce,
				Balance:      acc.StateAccount.Balance, // *uint256.Int
				BytecodeHash: nil,                      // EOA: no code
			}
			var accBuf bytes.Buffer
			ethAccount.EncodeCompact(&accBuf)
			accountBytes := accBuf.Bytes()

			// 1. PlainAccountState — raw addr → Account
			if err := txn.Put(envs.MdbxDBIs["PlainAccountState"], acc.Address[:], accountBytes, 0); err != nil {
				return fmt.Errorf("PlainAccountState %s: %w", acc.Address.Hex(), err)
			}

			// 2. HashedAccounts — keccak(addr) → Account
			if err := txn.Put(envs.MdbxDBIs["HashedAccounts"], acc.AddrHash[:], accountBytes, 0); err != nil {
				return fmt.Errorf("HashedAccounts %s: %w", acc.Address.Hex(), err)
			}

			if archive {
				// 3. AccountChangeSets — DupSort: BE_u64(block) → AccountBeforeTx{addr, nil}
				// Address is the DupSort SubKey (encoded first in AccountBeforeTx.EncodeCompact).
				// Info=nil: account had no prior state (genesis creation).
				abt := iReth.AccountBeforeTx{Address: acc.Address, Info: nil}
				var abtBuf bytes.Buffer
				abt.EncodeCompact(&abtBuf)
				if err := txn.Put(envs.MdbxDBIs["AccountChangeSets"], blockKey[:], abtBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("AccountChangeSets %s: %w", acc.Address.Hex(), err)
				}

				// 4. AccountsHistory — ShardedKey(addr, u64::MAX) → IntegerList([blockNum])
				// u64::MAX marks the latest (open) shard; the bitmap contains the block
				// numbers at which this account was first touched.
				shardedKey := iReth.ShardedKeyAddress{Address: acc.Address, BlockNumber: ^uint64(0)}
				var keyBuf bytes.Buffer
				shardedKey.EncodeKey(&keyBuf)
				var listBuf bytes.Buffer
				iReth.EncodeIntegerList(&listBuf, []uint64{blockNum})
				if err := txn.Put(envs.MdbxDBIs["AccountsHistory"], keyBuf.Bytes(), listBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("AccountsHistory %s: %w", acc.Address.Hex(), err)
				}
			}

			// Plain + Hashed writes always succeed; archive-only writes
			// inside the if-block fall through to here. Bank the row's
			// bytes locally; transferred to stats only if Update succeeds.
			localAccountBytes += uint64(len(accountBytes))
		}
		return nil
	})
	if err != nil {
		return err
	}
	if stats != nil {
		stats.AccountBytes += localAccountBytes
	}
	return nil
}
