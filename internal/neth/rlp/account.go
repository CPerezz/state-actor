// Package rlp wraps go-ethereum's RLP encoder for the subset of types
// state-actor's Nethermind writer emits. It exists so callers (B4 trie
// builder, B5 orchestration) interact with a single state-actor-owned
// surface — the encoders here are byte-equivalent to Nethermind's because
// both consume the same standard Ethereum RLP specification.
//
// Citations point at Nethermind upstream/master at SHA 09bd5a2d:
//
//   - AccountDecoder:    src/Nethermind/Nethermind.Serialization.Rlp/AccountDecoder.cs
package rlp

import (
	"github.com/ethereum/go-ethereum/core/types"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
)

// EncodeAccount returns the RLP-encoded bytes of an account in Nethermind's
// "full" format: [nonce, balance, storageRoot, codeHash].
//
// Mirrors AccountDecoder.cs:Encode (the non-slim branch). Nethermind's
// `slim` form skips StorageRoot/CodeHash when the account has no storage /
// code; state-actor never emits the slim form because the State DB stores
// full-form leaves.
//
// The byte output must match what types.StateAccount serializes via
// go-ethereum's RLP — both follow the standard Ethereum spec. Tests pin
// the empty-account bytes byte-for-byte to surface any drift.
func EncodeAccount(acc *types.StateAccount) ([]byte, error) {
	return gethrlp.EncodeToBytes(acc)
}
