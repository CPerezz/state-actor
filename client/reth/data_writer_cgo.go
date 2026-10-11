//go:build cgo_reth

package reth

import (
	"bytes"
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"

	iReth "github.com/ethereum/state-actor/internal/reth"

	"github.com/ethereum/state-actor/internal/entitygen"
)

// WriteEOAs writes the archive-mode rows for each EOA inside one atomic
// MDBX write transaction:
//   - AccountChangeSets (DupSort: BE_u64(block) → AccountBeforeTx{addr, nil}).
//   - AccountsHistory (ShardedKey(addr, u64::MAX) → IntegerList([blockNum]));
//     routed to the RocksDB CF via envs.HistorySink().
//
// HashedAccounts is not written here: RunCgo appends every account's row
// in addrHash order during phase 2 instead, so the table packs densely
// rather than filling pages in RNG draw order. Callers with archive=false
// and no other per-EOA side effect have nothing left to write — RunCgo
// skips calling WriteEOAs in that case.
//
// Accounts are written in input order (caller controls ordering).
func WriteEOAs(envs *Envs, accounts []*entitygen.Account, blockNum uint64, archive bool) error {
	if !archive {
		return nil
	}
	return envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		blockKey := beU64(blockNum)

		for _, acc := range accounts {
			if acc.StateAccount == nil {
				return fmt.Errorf("WriteEOAs: account %s has nil StateAccount", acc.Address.Hex())
			}

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
			// EitherReader::new_accounts_history). Wire format:
			// ShardedKeyAddress(addr, u64::MAX) → EncodeIntegerList([blockNum]).
			if err := envs.HistorySink().PutAccountHistory(acc.Address, blockNum); err != nil {
				return fmt.Errorf("AccountsHistory %s: %w", acc.Address.Hex(), err)
			}
		}
		return nil
	})
}
