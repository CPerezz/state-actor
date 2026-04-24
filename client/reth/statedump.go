package reth

import (
	"bytes"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/ethereum/go-ethereum/trie"
)

// finalizeStorageRoot computes the storage root for ad.storage and writes it
// into ad.account.Root. Also re-sorts storage slots by keccak256(key) as
// required by the MPT StackTrie. No-op when storage is empty.
//
// Called per-contract during streamAlloc so the account emitted into the
// chainspec carries the correct storageRoot for Reth to match against the
// alloc-computed root (Reth rebuilds the trie from alloc on `init`).
func finalizeStorageRoot(ad *accountData) error {
	if len(ad.storage) == 0 {
		ad.account.Root = types.EmptyRootHash
		return nil
	}
	type hashedSlot struct {
		keyHash common.Hash
		value   common.Hash
	}
	sorted := make([]hashedSlot, len(ad.storage))
	for i, s := range ad.storage {
		sorted[i] = hashedSlot{
			keyHash: crypto.Keccak256Hash(s.Key[:]),
			value:   s.Value,
		}
	}
	sort.Slice(sorted, func(i, j int) bool {
		return bytes.Compare(sorted[i].keyHash[:], sorted[j].keyHash[:]) < 0
	})
	st := trie.NewStackTrie(nil)
	for _, s := range sorted {
		rlpVal, err := encodeStorageValue(s.value)
		if err != nil {
			return err
		}
		if len(rlpVal) > 0 {
			st.Update(s.keyHash[:], rlpVal)
		}
	}
	ad.account.Root = st.Hash()
	return nil
}

// encodeStorageValue RLP-encodes a storage value with leading zero bytes
// trimmed. Zero values return nil bytes (caller must skip insertion).
func encodeStorageValue(v common.Hash) ([]byte, error) {
	trimmed := trimLeftZeroes(v[:])
	if len(trimmed) == 0 {
		return nil, nil
	}
	return rlp.EncodeToBytes(trimmed)
}

func trimLeftZeroes(s []byte) []byte {
	for i, v := range s {
		if v != 0 {
			return s[i:]
		}
	}
	return nil
}
