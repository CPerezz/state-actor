// Package streamingtrie computes a storage-trie root from a streaming
// per-entity slot iter, in bounded RAM regardless of slot count.
//
// The pipeline:
//
//  1. Drain the (slotKey, slotValue) iter.Seq2 into a per-entity
//     internal/streamsort.Store, hashing each slotKey to keccak(slotKey)
//     on the way in. Zero-valued slots are skipped (matches alloy-
//     genesis's filter(!value.is_zero()) and go-ethereum's snapshot
//     convention — keeps the storage-trie root in lockstep with what
//     a genesis-alloc-built fixture produces).
//
//  2. Iterate the sorted store. For each sorted (keyHash, rawKey, value)
//     triple:
//       a. Call the caller-supplied Sink with (keyHash, rawKey, value).
//          This is where the per-client storage-row DB writes happen
//          (reth's PlainStorageState + HashedStorages, nethermind's
//          Storage CF, besu's Bonsai flat-state, geth's snapshot).
//          The Sink receives BOTH the rawKey (for DupSort-style tables
//          that store the original slot key) AND the keyHash (for
//          hashed-storage tables).
//       b. Strip leading zeros from the slot value + RLP-encode the
//          result. This is the canonical MPT leaf-value encoding
//          (Yellow Paper §4.1; reth and alloy_trie::root::storage_root
//          do exactly the same trim+RLP). Centralising it here
//          guarantees all four clients consume identical bytes.
//       c. Feed (keyHash, RLP'd value) into the caller-supplied
//          HashBuilder. The builder yields the canonical storage MPT
//          root on Root().
//
//  3. Return hb.Root() — the storage trie root the per-client account
//     writer must splice into StateAccount.Root before the account leaf
//     is finalised.
//
// RAM is bounded at streamsort.MemTableSize (currently 2 GiB) regardless
// of slot count. Disk usage during the drain is O(slot_count × 96 B)
// in the temp Pebble dir; reclaimed on Store.Close().
//
// Re-iteration safety: pure-function iter.Seq2 producers (the templates
// package emits these) may be passed to StorageRoot multiple times with
// the same HashBuilder/Sink shape and produce identical state-trie
// roots — the function is referentially transparent w.r.t. its
// arguments.
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

// HashBuilder is the per-client streaming MPT builder contract. Each
// client's existing storage-trie builder type satisfies this either
// directly (reth's iReth.HashBuilder) or via a one-method wrapper
// (geth's trie.StackTrie, nethermind's builder, besu's builder).
//
// AddLeaf is called in keccak-ascending order with the 32-byte keyHash
// (already keccak'd by StorageRoot) and the RLP-encoded value bytes.
// Per-client wrappers convert keyHash to whatever path representation
// their internal trie wants (unpacked nibbles for reth; raw 32-byte
// keys for geth's StackTrie).
//
// Root is called once after the iteration completes and must return
// the canonical storage MPT root for the inputs received.
type HashBuilder interface {
	AddLeaf(keyHash common.Hash, valueRLP []byte) error
	Root() (common.Hash, error)
}

// Sink is invoked once per sorted (keyHash, rawKey, value) triple before
// the trie-leaf is appended. The per-client implementation writes the
// matching storage-row(s) into the destination DB:
//
//   - reth:        PlainStorageState (rawKey-keyed DupSort), HashedStorages
//                  (keyHash-keyed DupSort), StorageChangeSets, StoragesHistory.
//   - nethermind:  Storage CF rows keyed by HalfPath(addrHash || keyHash).
//   - besu:        Bonsai flat-state row keyed by addrHash || keyHash.
//   - geth:        Snapshot Pebble row keyed by addrHash || keyHash.
//
// Returning a non-nil error short-circuits the iteration; the error
// propagates out of StorageRoot.
type Sink func(keyHash, rawKey, value common.Hash) error

// StorageRoot drains the per-entity slot iter through a streamsort.Store,
// then walks the sorted output to drive both the per-client Sink (DB row
// writes) and the per-client HashBuilder (storage MPT root). Returns the
// storage trie root.
//
// workDir is passed through to streamsort.New; empty means os.TempDir().
//
// storage MUST yield deterministically — same inputs across runs and
// across machines. Templates' iter.Seq2 producers (internal/templates)
// satisfy this; non-template callers must too, otherwise the cross-
// client invariant breaks.
//
// hb MUST be a freshly-constructed builder (no prior AddLeaf calls);
// the function does not reset it.
//
// sink MAY be nil — useful for callers that only need the root (e.g.
// the dry-run path or unit tests). When nil, only the trie is built.
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
		// Empty storage → the builder hasn't seen any leaves → Root() is
		// the canonical empty-MPT root.
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

	// --- Drain phase: hash + store, skipping zero values. ---
	//
	// Layout in the streamsort.Store:
	//   key:   keccak(rawKey)            (32 bytes — sort key)
	//   value: rawKey[0..32] || value[0..32]  (64 bytes — rawKey kept for
	//                                          Sinks that index by it)
	var putErr error
	storage(func(k, v common.Hash) bool {
		if v == (common.Hash{}) {
			return true // skip zero-valued slots — canonical
		}
		keyHash := crypto.Keccak256Hash(k[:])
		var combined [64]byte
		copy(combined[0:32], k[:])
		copy(combined[32:64], v[:])
		if err := s.Put(keyHash[:], combined[:]); err != nil {
			putErr = fmt.Errorf("streamingtrie: drain Put: %w", err)
			return false // stop iteration
		}
		return true
	})
	if putErr != nil {
		return common.Hash{}, putErr
	}

	// --- Read-back phase: sorted iterate, sink + trie in lockstep. ---
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

// uint64SlotKey is a small helper used by tests to construct
// reproducible synthetic slot keys. Exposed via this package so
// internal/templates and per-client adapter tests can share the
// derivation.
func uint64SlotKey(slot uint64) common.Hash {
	var h common.Hash
	binary.BigEndian.PutUint64(h[24:32], slot)
	return h
}
