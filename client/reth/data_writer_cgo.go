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

// WriteEOAs writes data-table rows for each account: HashedAccounts (the
// canonical v2 account-state table), and (in archive mode) AccountChangeSets
// at blockNum + AccountsHistory.
//
// MDBX writes happen in ONE MDBX write transaction. The archive-mode
// AccountsHistory write goes into the RocksDB column family v2 reth reads
// from (envs.HistorySink); MDBX and RocksDB are independent backends so the
// atomicity boundary is per-backend, not cross-backend. On error after
// the RocksDB batch has accumulated, Envs.Close drains it; on caller
// error before Close the batch is dropped (an empty datadir is a safe
// abort state).
//
// Tables written per EOA:
//   - HashedAccounts (keccak(Address) → Account, MDBX) — always
//   - AccountChangeSets (DupSort: BE_u64(block) → AccountBeforeTx{addr, nil}, MDBX) — archive only
//   - AccountsHistory (ShardedKey(addr, u64::MAX) → IntegerList([blockNum]), RocksDB CF) — archive only
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

			// HashedAccounts — keccak(addr) → Account (canonical v2 state)
			if err := txn.Put(envs.MdbxDBIs["HashedAccounts"], acc.AddrHash[:], accountBytes, 0); err != nil {
				return fmt.Errorf("HashedAccounts %s: %w", acc.Address.Hex(), err)
			}

			if archive {
				// AccountChangeSets — DupSort: BE_u64(block) → AccountBeforeTx{addr, nil}
				// Address is the DupSort SubKey (encoded first in AccountBeforeTx.EncodeCompact).
				// Info=nil: account had no prior state (genesis creation).
				abt := iReth.AccountBeforeTx{Address: acc.Address, Info: nil}
				var abtBuf bytes.Buffer
				abt.EncodeCompact(&abtBuf)
				if err := txn.Put(envs.MdbxDBIs["AccountChangeSets"], blockKey[:], abtBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("AccountChangeSets %s: %w", acc.Address.Hex(), err)
				}

				// AccountsHistory → RocksDB CF (v2 routing per
				// either_writer.rs:741-759). Same encoding as the v1 MDBX
				// row; only the backend differs.
				if err := envs.HistorySink().PutAccountHistory(acc.Address, blockNum); err != nil {
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
