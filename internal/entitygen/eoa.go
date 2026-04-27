package entitygen

import (
	mrand "math/rand"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"
)

// GenerateEOA generates an Externally Owned Account using the supplied RNG.
//
// RNG draw order (NEVER reorder — golden hashes depend on it):
//  1. rng.Read(addr[:])         — 20 bytes for the address
//  2. rng.Intn(1000)             — balance multiplier (×1e18 wei)
//  3. rng.Intn(1000)             — nonce
//
// The returned Account has Storage/Code/CodeHash zero-valued; callers that
// need to deduplicate addresses against an existing set should iterate this
// function until the result's Address is unique. Each retry consumes a fresh
// 23-byte RNG window.
func GenerateEOA(rng *mrand.Rand) *Account {
	var addr common.Address
	rng.Read(addr[:])

	// Random balance between 0 and 1000 ETH
	balance := new(uint256.Int).Mul(
		uint256.NewInt(uint64(rng.Intn(1000))),
		uint256.NewInt(1e18),
	)

	return &Account{
		Address:  addr,
		AddrHash: crypto.Keccak256Hash(addr[:]),
		StateAccount: &types.StateAccount{
			Nonce:    uint64(rng.Intn(1000)),
			Balance:  balance,
			Root:     types.EmptyRootHash,
			CodeHash: types.EmptyCodeHash.Bytes(),
		},
	}
}
