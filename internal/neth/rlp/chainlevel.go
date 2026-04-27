package rlp

import (
	gethrlp "github.com/ethereum/go-ethereum/rlp"
)

// ChainLevelInfo is the value that lives at key=blockNumber in Nethermind's
// blockInfos DB. It tracks every fork that exists at this height and
// flags whether the main chain holds a block at this level.
//
// State-actor writes one of these per height — for genesis, that's
// `ChainLevelInfo{HasBlockOnMainChain: true, BlockInfos: [genesisBlockInfo]}`
// at key=0. Nethermind's BlockTree constructor reads this entry to decide
// whether the database already has a genesis block (if so, skip the loader).
type ChainLevelInfo struct {
	HasBlockOnMainChain bool
	BlockInfos          []*BlockInfo
}

// EncodeChainLevelInfo returns the RLP encoding of a ChainLevelInfo.
//
// Layout (per Nethermind.Serialization.Rlp/ChainLevelDecoder.cs:Encode):
//
//	[HasBlockOnMainChain, [BlockInfo, BlockInfo, ...]]
//
// Each BlockInfo is encoded by EncodeBlockInfo and embedded raw. A nil
// ChainLevelInfo encodes as the empty list 0xc0. Nil entries inside
// BlockInfos panic — Nethermind throws "BlockInfo is null when encoding
// ChainLevelInfo" in the same case.
func EncodeChainLevelInfo(cli *ChainLevelInfo) []byte {
	if cli == nil {
		return []byte{0xc0}
	}

	// Encode each BlockInfo to its own RLP, then assemble as a list of raw
	// values inside the inner list. This matches Nethermind's pattern of
	// encoding each BlockInfo with stream.Encode(blockInfo) inside the
	// outer StartSequence.
	inner := make([]gethrlp.RawValue, len(cli.BlockInfos))
	for i, bi := range cli.BlockInfos {
		if bi == nil {
			panic("EncodeChainLevelInfo: nil BlockInfo at index " +
				string(rune('0'+i)))
		}
		inner[i] = EncodeBlockInfo(bi)
	}

	out, err := gethrlp.EncodeToBytes([]any{cli.HasBlockOnMainChain, inner})
	if err != nil {
		panic(err)
	}
	return out
}
