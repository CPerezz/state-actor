//go:build cgo_reth

package reth

import (
	"bytes"
	"encoding/json"
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
//   - Metadata.storage_settings = JSON-encoded StorageSettings (the key reth
//     reads via STORAGE_SETTINGS const at storage-api/src/metadata.rs:10).
//     The value is {"storage_v2":false} so reth selects v1 (matches the
//     current write surface; commit 3 of the v2 migration flips this).
//   - StageCheckpoints: one entry per stage in iReth.StageIDsAll (15 entries),
//     Compact-encoded StageCheckpoint{BlockNumber: 0}.
//   - HeaderNumbers: header.Hash() → BE u64(0).
//   - BlockBodyIndices: BE u64(0) → Compact StoredBlockBodyIndices{0, 0}.
//   - PruneCheckpoints (NON-ARCHIVE ONLY): two rows for AccountHistory +
//     StorageHistory with block_number=Some(0), prune_mode=Before(1).
//     Triggers reth's HistoricalStateProvider.MaybeInPlainState branch
//     (historical.rs:861-867) so eth_getBalance on genesis accounts reads
//     PlainAccountState directly instead of returning NotYetWritten when
//     history-index tables are empty in non-archive mode.
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
		if err := writeStorageSettings(txn, envs.MdbxDBIs["Metadata"]); err != nil {
			return fmt.Errorf("Metadata.storage_settings: %w", err)
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

// storageSettings mirrors reth's StorageSettings struct
// (crates/storage/db-api/src/models/metadata.rs:13-27). Single bool field, no
// serde rename — serde_json::to_vec produces {"storage_v2":<bool>} (19 or 20
// bytes, no whitespace). State-actor encodes/decodes via encoding/json so the
// Go type drift stays linked to the wire format.
type storageSettings struct {
	StorageV2 bool `json:"storage_v2"`
}

// writeStorageSettings puts a JSON-encoded storageSettings under the key
// "storage_settings" in the Metadata table — the key reth's
// StorageSettingsCache::load (storage-api/src/metadata.rs:10) reads. The
// value is {"storage_v2":false}: state-actor still writes the v1 layout
// (PlainAccountState/PlainStorageState populated). Commit 3 of the v2
// migration flips the flag to true and drops the Plain* writes.
func writeStorageSettings(txn *mdbx.Txn, dbi mdbx.DBI) error {
	val, err := json.Marshal(storageSettings{StorageV2: false})
	if err != nil {
		return fmt.Errorf("marshal storageSettings: %w", err)
	}
	return txn.Put(dbi, []byte("storage_settings"), val, 0)
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
