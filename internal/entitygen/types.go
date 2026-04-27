// Package entitygen provides deterministic, RNG-driven primitives for
// generating Ethereum-shaped state-actor entities (EOAs, contracts, storage
// slots) shared across client backends (geth, reth, nethermind).
//
// All primitives take a *math/rand.Rand explicitly so the caller controls the
// RNG seed and lifetime. Determinism is the package's load-bearing contract:
// the exact sequence of rng.Read / rng.Intn / rng.Float64 / rng.Int63 calls
// must not change between releases without coordinated golden-hash updates
// across all client backends. The sequence is what produces matching state
// roots for the same --seed across geth, reth, nethermind.
//
// The package is internal/ — only state-actor's own packages may import it.
package entitygen

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// StorageSlot is a key-value pair for deterministic storage iteration.
// Slots returned from this package are pre-sorted by Key so consumers can
// stream them into a StackTrie without re-sorting.
type StorageSlot struct {
	Key   common.Hash
	Value common.Hash
}

// Account is a generated state-actor entity. EOAs leave Code/CodeHash/Storage
// empty; contracts populate all fields.
//
// StateAccount is the go-ethereum struct that ultimately gets RLP-encoded
// into the state trie leaf — entitygen builds it directly so consumers can
// pass it through to their writer without re-deriving Nonce/Balance/Root/CodeHash.
type Account struct {
	Address      common.Address
	AddrHash     common.Hash
	StateAccount *types.StateAccount
	Code         []byte
	CodeHash     common.Hash
	Storage      []StorageSlot // pre-sorted by Key
}
