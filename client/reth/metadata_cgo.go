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
//
// Writes 5 tables in a single atomic transaction:
//   - Metadata.storage_v2 = Compact-encoded StorageSettings{storage_v2: true}
//     (1-byte bitflag header with the single bit set = 0x01). Verified
//     against reth crates/storage/db-api/src/models/metadata.rs at
//     PinnedRethCommit: StorageSettings is a single-bool struct, so
//     Compact encoding is exactly a 1-bit bitflag header byte.
//   - StageCheckpoints: one entry per stage in iReth.StageIDsAll (15 entries),
//     Compact-encoded StageCheckpoint{BlockNumber: 0}.
//   - HeaderNumbers: header.Hash() → BE u64(0).
//   - BlockBodyIndices: BE u64(0) → Compact StoredBlockBodyIndices{0, 0}.
//   - VersionHistory: BE u64(0) → Compact ClientVersion identity.
//
// ChainState is left empty; reth populates it lazily on finality.
//
// NOTE: the Number=0 guard below is a forward-compatibility trap for Slices
// D+E. If a future slice switches to a non-genesis header (e.g. block 1 to
// work around an underflow bug), this guard must be relaxed or replaced.
func WriteMetadata(envs *Envs, header *types.Header, chainID uint64) error {
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
		if err := writeVersionHistory(txn, envs.MdbxDBIs["VersionHistory"]); err != nil {
			return fmt.Errorf("VersionHistory: %w", err)
		}
		return nil
	})
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

// writeVersionHistory writes a state-actor identity ClientVersion under
// the all-zero BE u64 key (sentinel "first write") in VersionHistory.
func writeVersionHistory(txn *mdbx.Txn, dbi mdbx.DBI) error {
	cv := iReth.ClientVersion{
		Version:        "state-actor-direct-write",
		GitSha:         iReth.PinnedRethCommit,
		BuildTimestamp: "2026-04-30",
	}
	var buf bytes.Buffer
	cv.EncodeCompact(&buf)
	key := beU64(0)
	return txn.Put(dbi, key[:], buf.Bytes(), 0)
}

// beU64 encodes v as 8 big-endian bytes.
func beU64(v uint64) [8]byte {
	return [8]byte{
		byte(v >> 56), byte(v >> 48), byte(v >> 40), byte(v >> 32),
		byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v),
	}
}
