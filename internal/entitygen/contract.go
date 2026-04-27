package entitygen

import (
	"bytes"
	mrand "math/rand"
	"sort"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// GenerateContract generates a contract account with code and storage using
// the supplied RNG.
//
// RNG draw order (NEVER reorder — golden hashes depend on it):
//  1. rng.Read(addr[:])             — 20 bytes for the address
//  2. rng.Intn(codeSize)             — extra code length (codeSize + extra = total)
//  3. rng.Read(code)                 — codeSize+extra bytes of code
//  4. rng.Intn(100)                  — balance multiplier (×1e18 wei)
//  5. for each of numSlots:
//       a. rng.Read(key[:])           — 32 bytes
//       b. rng.Read(value[:])         — 32 bytes (zero-valued bumped to 0x..01)
//  6. rng.Intn(1000)                  — nonce (after the slot loop)
//
// The returned Account.Storage is sorted by Key so consumers can stream into a
// StackTrie without re-sorting.
func GenerateContract(rng *mrand.Rand, codeSize int, numSlots int) *Account {
	var addr common.Address
	rng.Read(addr[:])

	// Generate random code
	totalCodeSize := codeSize + rng.Intn(codeSize)
	code := make([]byte, totalCodeSize)
	rng.Read(code)
	codeHash := crypto.Keccak256Hash(code)

	// Random balance
	balance := new(uint256.Int).Mul(
		uint256.NewInt(uint64(rng.Intn(100))),
		uint256.NewInt(1e18),
	)

	// Generate storage slots as a pre-sorted slice for deterministic trie insertion.
	storage := make([]StorageSlot, 0, numSlots)
	for j := 0; j < numSlots; j++ {
		var key, value common.Hash
		rng.Read(key[:])
		rng.Read(value[:])
		// Ensure value is non-zero (zero values are deletions)
		if value == (common.Hash{}) {
			value[31] = 1
		}
		storage = append(storage, StorageSlot{Key: key, Value: value})
	}
	sort.Slice(storage, func(i, j int) bool {
		return bytes.Compare(storage[i].Key[:], storage[j].Key[:]) < 0
	})

	return &Account{
		Address:  addr,
		AddrHash: crypto.Keccak256Hash(addr[:]),
		StateAccount: &types.StateAccount{
			Nonce:    uint64(rng.Intn(1000)),
			Balance:  balance,
			Root:     types.EmptyRootHash, // Will be computed
			CodeHash: codeHash.Bytes(),
		},
		Code:     code,
		CodeHash: codeHash,
		Storage:  storage,
	}
}
