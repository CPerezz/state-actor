// Package streamingtrie computes a storage-trie root from a streaming
// per-entity slot iter, in bounded RAM regardless of slot count.
//
// Pipeline:
//
//  1. Drain (slotKey, value) into a streamsort.Store keyed by
//     keccak(slotKey). Zero-valued slots are skipped (canonical).
//  2. Iterate sorted; for each entry call the Sink (per-client DB row
//     writes), then feed (keyHash, trim+RLP(value)) to the HashBuilder.
//  3. Return HashBuilder.Root().
//
// RAM is bounded by streamsort.MemTableSize. Disk usage during the
// drain is O(slot_count × 96 B) in the temp Pebble dir.
//
// Producers that are pure functions of their inputs may be passed to
// StorageRoot multiple times and produce identical roots.
package streamingtrie

import (
	"encoding/binary"
	"fmt"
	"iter"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"

	"github.com/nerolation/state-actor/internal/streamsort"
)

// HashBuilder is the per-client streaming MPT builder contract.
// AddLeaf is called in keccak-ascending order with the 32-byte keyHash
// (already keccak'd by StorageRoot) and the RLP-encoded value bytes.
// Root is called once after iteration completes.
type HashBuilder interface {
	AddLeaf(keyHash common.Hash, valueRLP []byte) error
	Root() (common.Hash, error)
}

// Sink is invoked once per sorted (keyHash, rawKey, value) triple
// before the trie-leaf is appended. Non-nil error short-circuits.
type Sink func(keyHash, rawKey, value common.Hash) error

// StorageRoot drains storage into a streamsort.Store, walks the sorted
// output to drive Sink and HashBuilder in lockstep, and returns the
// storage trie root.
//
// workDir is passed through to streamsort.New (empty → os.TempDir()).
// storage MUST yield deterministically. hb MUST be freshly constructed.
// sink MAY be nil (root-only path).
func StorageRoot(
	workDir string,
	storage iter.Seq2[common.Hash, common.Hash],
	hb HashBuilder,
	sink Sink,
) (common.Hash, error) {
	if hb == nil {
		return common.Hash{}, fmt.Errorf("streamingtrie: nil HashBuilder")
	}
	if storage == nil {
		root, err := hb.Root()
		if err != nil {
			return common.Hash{}, fmt.Errorf("streamingtrie: Root: %w", err)
		}
		return root, nil
	}

	s, err := streamsort.New(workDir)
	if err != nil {
		return common.Hash{}, fmt.Errorf("streamingtrie: streamsort.New: %w", err)
	}
	defer s.Close()

	// Store layout: key=keccak(rawKey) (32 B), value=rawKey||value (64 B).
	var putErr error
	storage(func(k, v common.Hash) bool {
		if v == (common.Hash{}) {
			return true
		}
		keyHash := crypto.Keccak256Hash(k[:])
		var combined [64]byte
		copy(combined[0:32], k[:])
		copy(combined[32:64], v[:])
		if err := s.Put(keyHash[:], combined[:]); err != nil {
			putErr = fmt.Errorf("streamingtrie: drain Put: %w", err)
			return false
		}
		return true
	})
	if putErr != nil {
		return common.Hash{}, putErr
	}

	if err := s.Iterate(func(keyHashB, combinedB []byte) error {
		var keyHash, rawKey, value common.Hash
		copy(keyHash[:], keyHashB)
		copy(rawKey[:], combinedB[0:32])
		copy(value[:], combinedB[32:64])

		if sink != nil {
			if err := sink(keyHash, rawKey, value); err != nil {
				return fmt.Errorf("streamingtrie: sink: %w", err)
			}
		}

		valBytes := value[:]
		for len(valBytes) > 0 && valBytes[0] == 0 {
			valBytes = valBytes[1:]
		}
		valRLP, err := rlp.EncodeToBytes(valBytes)
		if err != nil {
			return fmt.Errorf("streamingtrie: rlp encode value: %w", err)
		}
		if err := hb.AddLeaf(keyHash, valRLP); err != nil {
			return fmt.Errorf("streamingtrie: AddLeaf: %w", err)
		}
		return nil
	}); err != nil {
		return common.Hash{}, err
	}

	root, err := hb.Root()
	if err != nil {
		return common.Hash{}, fmt.Errorf("streamingtrie: Root: %w", err)
	}
	return root, nil
}

func uint64SlotKey(slot uint64) common.Hash {
	var h common.Hash
	binary.BigEndian.PutUint64(h[24:32], slot)
	return h
}
