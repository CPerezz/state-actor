// Package rlp implements the RLP encoders for Besu Bonsai wire formats that
// state-actor's Besu writer emits. It is dependency-free at the runtime layer:
// no I/O, no goroutines, no database handles.
//
// Citations: BonsaiAccount.java:155-164, PathBasedAccount.java:254-258 (tag 26.5.0).
package rlp

import (
	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
	"github.com/holiman/uint256"
)

// EncodeAccount returns the Besu Bonsai flat-state RLP for one account.
//
// Field order (BonsaiAccount.writeTo / PathBasedAccount.serializeAccount):
//
//	RLP_LIST [ nonce_scalar, balance_scalar, storageRoot_bytes32, codeHash_bytes32 ]
//
// This is byte-identical to go-ethereum's types.StateAccount RLP because both
// implement the canonical Ethereum account encoding (Yellow Paper §4.1). The
// function accepts fields separately rather than a *types.StateAccount so
// callers (Part 3 adapter) can construct the encoding without allocating an
// intermediate geth struct.
//
//   - nonce       — account transaction count (0 encodes as 0x80).
//   - balance     — account balance in wei (nil treated as zero).
//   - storageRoot — keccak256 root of the storage trie; EmptyTrieNodeHash for EOAs.
//   - codeHash    — keccak256 of the contract bytecode; EmptyCodeHash for EOAs.
func EncodeAccount(
	nonce uint64,
	balance *uint256.Int,
	storageRoot common.Hash,
	codeHash common.Hash,
) ([]byte, error) {
	if balance == nil {
		balance = uint256.NewInt(0)
	}
	acc := &types.StateAccount{
		Nonce:    nonce,
		Balance:  balance,
		Root:     storageRoot,
		CodeHash: codeHash.Bytes(),
	}
	return gethrlp.EncodeToBytes(acc)
}
