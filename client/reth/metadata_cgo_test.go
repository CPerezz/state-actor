//go:build cgo_reth

package reth

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/core/types"

	iReth "github.com/nerolation/state-actor/internal/reth"
)

func TestWriteMetadataAllTables(t *testing.T) {
	tmp := t.TempDir()
	envs, err := OpenEnvs(tmp, true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()

	header := &types.Header{
		Number: big.NewInt(0),
	}
	chainID := uint64(1337)

	if err := WriteMetadata(envs, header, chainID); err != nil {
		t.Fatalf("WriteMetadata: %v", err)
	}

	// Verify Metadata.storage_v2
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		val, err := txn.Get(envs.MdbxDBIs["Metadata"], []byte("storage_v2"))
		if err != nil {
			return err
		}
		// Compact bool true = 1-byte 0x01 (1-bit bitflag header).
		if !bytes.Equal(val, []byte{0x01}) {
			t.Errorf("Metadata[storage_v2] = %x, want 01", val)
		}
		return nil
	}); err != nil {
		t.Errorf("verify Metadata: %v", err)
	}

	// Verify all 15 StageCheckpoints at block 0
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		for _, stage := range iReth.StageIDsAll {
			val, err := txn.Get(envs.MdbxDBIs["StageCheckpoints"], []byte(stage))
			if err != nil {
				return err
			}
			var sc iReth.StageCheckpoint
			sc.DecodeCompact(val, len(val))
			if sc.BlockNumber != 0 {
				t.Errorf("StageCheckpoints[%s] block_number = %d, want 0", stage, sc.BlockNumber)
			}
		}
		return nil
	}); err != nil {
		t.Errorf("verify StageCheckpoints: %v", err)
	}

	// Verify HeaderNumbers
	expectedHash := header.Hash()
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		val, err := txn.Get(envs.MdbxDBIs["HeaderNumbers"], expectedHash[:])
		if err != nil {
			return err
		}
		if len(val) != 8 {
			t.Errorf("HeaderNumbers value len = %d, want 8", len(val))
		}
		// All-zero BE u64 = block 0
		for i, b := range val {
			if b != 0 {
				t.Errorf("HeaderNumbers value byte %d = %#x, want 0", i, b)
			}
		}
		return nil
	}); err != nil {
		t.Errorf("verify HeaderNumbers: %v", err)
	}

	// Verify BlockBodyIndices
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		key := []byte{0, 0, 0, 0, 0, 0, 0, 0} // BE u64 of 0
		val, err := txn.Get(envs.MdbxDBIs["BlockBodyIndices"], key)
		if err != nil {
			return err
		}
		var bbi iReth.StoredBlockBodyIndices
		bbi.DecodeCompact(val, len(val))
		if bbi.FirstTxNum != 0 || bbi.TxCount != 0 {
			t.Errorf("BlockBodyIndices = %+v, want {0, 0}", bbi)
		}
		return nil
	}); err != nil {
		t.Errorf("verify BlockBodyIndices: %v", err)
	}

	// Verify VersionHistory
	if err := envs.Mdbx.View(func(txn *mdbx.Txn) error {
		key := []byte{0, 0, 0, 0, 0, 0, 0, 0}
		val, err := txn.Get(envs.MdbxDBIs["VersionHistory"], key)
		if err != nil {
			return err
		}
		var cv iReth.ClientVersion
		cv.DecodeCompact(val, len(val))
		if cv.Version != "state-actor-direct-write" {
			t.Errorf("ClientVersion.Version = %q, want %q", cv.Version, "state-actor-direct-write")
		}
		if cv.GitSha != iReth.PinnedRethCommit {
			t.Errorf("ClientVersion.GitSha = %q, want %q", cv.GitSha, iReth.PinnedRethCommit)
		}
		return nil
	}); err != nil {
		t.Errorf("verify VersionHistory: %v", err)
	}
}
