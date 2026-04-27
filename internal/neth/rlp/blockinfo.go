package rlp

import (
	"math/big"

	gethrlp "github.com/ethereum/go-ethereum/rlp"
)

// BlockInfo mirrors Nethermind's BlockInfo: hash + processed flag +
// total-difficulty + optional metadata. Stored inside a ChainLevelInfo
// (which holds an array of these for every fork at a given block height).
//
// For state-actor's genesis use, BlockInfo is written with WasProcessed=true
// — that flag is the boot-detection contract: BlockTree.Genesis is set
// from disk only if BlockInfos[0].WasProcessed == true. See
// `Nethermind.Blockchain/BlockTree.cs:152-171, :964`.
type BlockInfo struct {
	BlockHash       [32]byte
	WasProcessed    bool
	TotalDifficulty *big.Int // nil treated as zero
	// Metadata is the BlockMetadata enum value (0 = None). When 0 the
	// field is OMITTED from the RLP — Nethermind's decoder uses
	// "did the stream end?" to detect absence rather than a sentinel
	// value, so we must match that on the wire.
	//
	// Stored as uint32 because go-ethereum's RLP encoder rejects signed
	// integer types. Nethermind treats this field as a 32-bit flags enum
	// (BlockMetadata: Finalized=1, Invalid=2, BeaconHeader=4, BeaconBody=8,
	// BeaconMainChain=16) — all non-negative, so the unsigned representation
	// is wire-compatible.
	Metadata uint32
}

// EncodeBlockInfo returns the RLP encoding of a BlockInfo.
//
// Layout (per Nethermind.Serialization.Rlp/BlockInfoDecoder.cs:Encode):
//
//	[BlockHash, WasProcessed, TotalDifficulty]                  (Metadata == 0)
//	[BlockHash, WasProcessed, TotalDifficulty, Metadata]        (Metadata != 0)
//
// nil BlockInfo encodes as the empty list 0xc0 (matching Nethermind's
// behavior of `stream.Encode(Rlp.OfEmptyList)` for null items).
func EncodeBlockInfo(bi *BlockInfo) []byte {
	if bi == nil {
		return []byte{0xc0}
	}

	td := bi.TotalDifficulty
	if td == nil {
		td = new(big.Int)
	}

	items := []any{
		bi.BlockHash[:],
		bi.WasProcessed,
		td,
	}
	if bi.Metadata != 0 {
		items = append(items, bi.Metadata)
	}

	out, err := gethrlp.EncodeToBytes(items)
	if err != nil {
		panic(err)
	}
	return out
}
