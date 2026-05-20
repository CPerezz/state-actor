//go:build cgo_reth

package reth

import (
	"bytes"
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	iReth "github.com/nerolation/state-actor/internal/reth"
)

// WriteMetadata populates the minimum-boot MDBX metadata into envs.
// header is the genesis header (block 0). chainID is reth's chain ID.
// archive controls whether to write PruneCheckpoint markers (see below).
//
// Writes the following tables in a single atomic transaction:
//   - Metadata.storage_v2 = Compact-encoded StorageSettings{storage_v2: true}
//     (1-byte bitflag header with the single bit set = 0x01).
//   - StageCheckpoints: one entry per stage in iReth.StageIDsAll (15 entries),
//     Compact-encoded StageCheckpoint{BlockNumber: 0}.
//   - HeaderNumbers: header.Hash() → BE u64(0).
//   - BlockBodyIndices: BE u64(0) → Compact StoredBlockBodyIndices{0, 0}.
//   - PruneCheckpoints (NON-ARCHIVE ONLY): two rows for AccountHistory +
//     StorageHistory with block_number=Some(0), prune_mode=Before(1).
//     This triggers reth's HistoricalStateProvider to use the
//     MaybeInPlainState fallback when the history-index tables are empty —
//     critical because state-actor doesn't write those tables in
//     non-archive mode (gated by `if archive`), and without this marker
//     reth returns NotYetWritten → eth_getBalance returns 0 for any
//     genesis account once the chain advances past genesis.
//     See plan: /Users/random_anon/.claude/plans/on-the-meantime-i-proud-karp.md
//
// VersionHistory is intentionally NOT written here. Reth's init_db writes its
// own ClientVersion entry keyed by the current Unix timestamp on every boot.
// ChainState is left empty; reth populates it lazily on finality.
//
// NOTE: the Number=0 guard below is a forward-compatibility trap. If a future
// caller switches to a non-genesis header, this guard must be relaxed.
func WriteMetadata(envs *Envs, header *types.Header, chainID uint64, archive bool) error {
	if header.Number.Sign() != 0 {
		return fmt.Errorf("WriteMetadata: header must be block 0, got %s", header.Number)
	}
	return envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		if err := writeStorageV2Flag(txn, envs.MdbxDBIs["Metadata"]); err != nil {
			return fmt.Errorf("Metadata.storage_v2: %w", err)
		}
		if err := writeStageCheckpoints(txn, envs.MdbxDBIs["StageCheckpoints"], 0); err != nil {
			return fmt.Errorf("StageCheckpoints: %w", err)
		}
		if err := writeHeaderNumber(txn, envs.MdbxDBIs["HeaderNumbers"], header.Hash(), 0); err != nil {
			return fmt.Errorf("HeaderNumbers: %w", err)
		}
		if err := writeBlockBodyIndices(txn, envs.MdbxDBIs["BlockBodyIndices"], 0); err != nil {
			return fmt.Errorf("BlockBodyIndices: %w", err)
		}
		if !archive {
			if err := writePruneCheckpoints(txn, envs.MdbxDBIs["PruneCheckpoints"]); err != nil {
				return fmt.Errorf("PruneCheckpoints: %w", err)
			}
		}
		return nil
	})
}

// writePruneCheckpoints writes the two non-archive-mode PruneCheckpoint rows
// for AccountHistory and StorageHistory. Together they trigger reth's
// MaybeInPlainState read-path fallback (see WriteMetadata doc).
//
// Both rows use the same value: PruneCheckpoint{
//
//	block_number: Some(0),
//	tx_number:    None,
//	prune_mode:   PruneMode::Before(1),
//
// }. The block_number-is-Some bit is what flips reth's history_info() from
// NotYetWritten to MaybeInPlainState; the prune_mode value is informational
// (mirrors what reth's own pruner would write after a single run).
func writePruneCheckpoints(txn *mdbx.Txn, dbi mdbx.DBI) error {
	ckpt := iReth.PruneCheckpoint{
		BlockNumber: iReth.U64Ptr(0),
		TxNumber:    nil,
		PruneMode:   iReth.PruneMode{Kind: iReth.PruneModeBefore, Value: 1},
	}
	var valBuf bytes.Buffer
	ckpt.EncodeCompact(&valBuf)
	value := valBuf.Bytes()

	for _, segment := range []uint8{iReth.PruneSegmentAccountHistory, iReth.PruneSegmentStorageHistory} {
		key := iReth.EncodePruneSegmentKey(segment)
		if err := txn.Put(dbi, key, value, 0); err != nil {
			return fmt.Errorf("segment %d: %w", segment, err)
		}
	}
	return nil
}

// writeStorageV2Flag puts Compact-encoded StorageSettings{storage_v2: true}
// (single byte 0x01) under the key "storage_v2" in the Metadata table.
//
// StorageSettings has one bool field. Compact derives a 1-bit bitflag header
// (padded to 1 byte). storage_v2=true sets that bit → 0x01.
func writeStorageV2Flag(txn *mdbx.Txn, dbi mdbx.DBI) error {
	return txn.Put(dbi, []byte("storage_v2"), []byte{0x01}, 0)
}

// writeStageCheckpoints writes one StageCheckpoint{BlockNumber: blockNum}
// per stage in iReth.StageIDsAll, Compact-encoded, into the StageCheckpoints
// table.
func writeStageCheckpoints(txn *mdbx.Txn, dbi mdbx.DBI, blockNum uint64) error {
	for _, stage := range iReth.StageIDsAll {
		sc := iReth.StageCheckpoint{BlockNumber: blockNum}
		var buf bytes.Buffer
		sc.EncodeCompact(&buf)
		if err := txn.Put(dbi, []byte(stage), buf.Bytes(), 0); err != nil {
			return fmt.Errorf("stage %q: %w", stage, err)
		}
	}
	return nil
}

// writeHeaderNumber puts hash → BE u64(num) into HeaderNumbers.
func writeHeaderNumber(txn *mdbx.Txn, dbi mdbx.DBI, hash common.Hash, num uint64) error {
	val := beU64(num)
	return txn.Put(dbi, hash[:], val[:], 0)
}

// writeBlockBodyIndices puts BE_u64(blockNum) → Compact(StoredBlockBodyIndices{0, 0})
// into BlockBodyIndices.
func writeBlockBodyIndices(txn *mdbx.Txn, dbi mdbx.DBI, blockNum uint64) error {
	bbi := iReth.StoredBlockBodyIndices{FirstTxNum: 0, TxCount: 0}
	var buf bytes.Buffer
	bbi.EncodeCompact(&buf)
	key := beU64(blockNum)
	return txn.Put(dbi, key[:], buf.Bytes(), 0)
}

// beU64 encodes v as 8 big-endian bytes.
func beU64(v uint64) [8]byte {
	return [8]byte{
		byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32),
		byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v),
	}
}
