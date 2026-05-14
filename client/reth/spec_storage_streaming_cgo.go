//go:build cgo_reth

package reth

import (
	"bytes"
	"context"
	"fmt"

	"github.com/erigontech/mdbx-go/mdbx"
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/generator"
	iReth "github.com/nerolation/state-actor/internal/reth"
	"github.com/nerolation/state-actor/internal/streamingtrie"
)

// streamSpecStorage streams each PreAlloc entity's Storage iter into
// the four reth storage tables (PlainStorageState, HashedStorages,
// StorageChangeSets, StoragesHistory), computes the storage MPT root,
// and splices it into cfg.GenesisAccounts[addr].Root so the subsequent
// alloc handler encodes the correct account leaf.
//
// stats.StorageBytes is updated only after the MDBX transaction commits.
func streamSpecStorage(ctx context.Context, envs *Envs, cfg *generator.Config, stats *generator.Stats) error {
	if len(cfg.PreAlloc) == 0 {
		return nil
	}
	var totalStorageBytes uint64
	err := envs.Mdbx.Update(func(txn *mdbx.Txn) error {
		var localStorageBytes uint64
		for i, pe := range cfg.PreAlloc {
			if pe.Storage == nil {
				continue
			}
			if err := ctx.Err(); err != nil {
				return err
			}
			addr := pe.Address
			addrHash := crypto.Keccak256Hash(addr[:])
			blockKey := iReth.BlockNumberAddress{BlockNumber: 0, Address: addr}
			var blockKeyBuf bytes.Buffer
			blockKey.EncodeKey(&blockKeyBuf)
			blockKeyBytes := blockKeyBuf.Bytes()

			sink := func(keyHash, rawKey, value common.Hash) error {
				slotValueU256 := uint256.NewInt(0).SetBytes(value[:])

				plainEntry := iReth.StorageEntry{Key: rawKey, Value: slotValueU256}
				var plainBuf bytes.Buffer
				plainEntry.EncodeCompact(&plainBuf)
				plainEntryBytes := plainBuf.Bytes()
				if err := txn.Put(envs.MdbxDBIs["PlainStorageState"], addr[:], plainEntryBytes, 0); err != nil {
					return fmt.Errorf("PlainStorageState %s slot %s: %w", addr.Hex(), rawKey.Hex(), err)
				}

				hashedEntry := iReth.StorageEntry{Key: keyHash, Value: slotValueU256}
				var hashedBuf bytes.Buffer
				hashedEntry.EncodeCompact(&hashedBuf)
				if err := txn.Put(envs.MdbxDBIs["HashedStorages"], addrHash[:], hashedBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("HashedStorages %s slot %s: %w", addrHash.Hex(), rawKey.Hex(), err)
				}

				changeEntry := iReth.StorageEntry{Key: rawKey, Value: uint256.NewInt(0)}
				var changeBuf bytes.Buffer
				changeEntry.EncodeCompact(&changeBuf)
				if err := txn.Put(envs.MdbxDBIs["StorageChangeSets"], blockKeyBytes, changeBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("StorageChangeSets %s slot %s: %w", addr.Hex(), rawKey.Hex(), err)
				}

				ssk := iReth.StorageShardedKey{
					Address:     addr,
					StorageKey:  rawKey,
					BlockNumber: ^uint64(0),
				}
				var sskBuf bytes.Buffer
				ssk.EncodeKey(&sskBuf)
				var listBuf bytes.Buffer
				iReth.EncodeIntegerList(&listBuf, []uint64{0})
				if err := txn.Put(envs.MdbxDBIs["StoragesHistory"], sskBuf.Bytes(), listBuf.Bytes(), 0); err != nil {
					return fmt.Errorf("StoragesHistory %s slot %s: %w", addr.Hex(), rawKey.Hex(), err)
				}
				localStorageBytes += uint64(len(plainEntryBytes))
				return nil
			}

			hb := newRethStorageHashBuilder()
			root, err := streamingtrie.StorageRoot(cfg.DBPath, pe.Storage, hb, sink)
			if err != nil {
				return fmt.Errorf("reth: stream spec storage[%d] %s: %w", i, addr.Hex(), err)
			}
			if acc, ok := cfg.GenesisAccounts[addr]; ok && acc != nil {
				acc.Root = root
			}
		}
		totalStorageBytes = localStorageBytes
		return nil
	})
	if err != nil {
		return err
	}
	if stats != nil {
		stats.StorageBytes += totalStorageBytes
	}
	return nil
}

// rethStorageHashBuilder adapts iReth.HashBuilder to streamingtrie.HashBuilder.
type rethStorageHashBuilder struct {
	hb *iReth.HashBuilder
}

func newRethStorageHashBuilder() *rethStorageHashBuilder {
	return &rethStorageHashBuilder{
		hb: iReth.NewHashBuilder(func(_ iReth.StoredNibbles, _ iReth.BranchNodeCompact) error {
			return nil
		}),
	}
}

func (r *rethStorageHashBuilder) AddLeaf(keyHash common.Hash, valueRLP []byte) error {
	return r.hb.AddLeaf(addrHashToNibbles(keyHash[:]), valueRLP)
}

func (r *rethStorageHashBuilder) Root() (common.Hash, error) {
	return r.hb.Root(), nil
}
