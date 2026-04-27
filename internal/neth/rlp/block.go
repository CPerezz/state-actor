package rlp

import (
	"github.com/ethereum/go-ethereum/core/types"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
)

// EncodeBlock returns the RLP encoding of a full block:
//
//	[header, transactions, uncles, withdrawals?]
//
// For state-actor's genesis use, transactions and uncles are empty and
// withdrawals is nil (or empty if Shanghai is active at genesis). The
// encoded bytes are what Nethermind's BlockDecoder expects when reading
// from the blocks DB at key=(numBE(8)||hash(32)).
//
// Wraps go-ethereum's `*types.Block`. Same standard-RLP rationale as
// EncodeHeader.
func EncodeBlock(b *types.Block) ([]byte, error) {
	return gethrlp.EncodeToBytes(b)
}
