//go:build cgo_neth

package nethermind

import (
	"encoding/binary"
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/linxGnu/grocksdb"

	"github.com/nerolation/state-actor/internal/neth"
	nethrlp "github.com/nerolation/state-actor/internal/neth/rlp"
)

// blockNumPrefixedKey is GetBlockNumPrefixedKey from
// Nethermind.Core/KeyValueStoreExtensions.cs:
//
//	public static void GetBlockNumPrefixedKey(long blockNumber, ValueHash256 blockHash, Span<byte> output)
//	{
//	    blockNumber.WriteBigEndian(output);
//	    blockHash!.Bytes.CopyTo(output[8..]);
//	}
//
// Output is exactly 40 bytes: 8-byte big-endian number followed by the
// 32-byte hash. Used as the key for blocks/, headers/, and receipts/Blocks.
func blockNumPrefixedKey(blockNumber uint64, hash common.Hash) []byte {
	out := make([]byte, 40)
	binary.BigEndian.PutUint64(out[0:8], blockNumber)
	copy(out[8:40], hash[:])
	return out
}

// blockNumKeyWithoutLeadingZeros mirrors
// Int64Extensions.ToBigEndianSpanWithoutLeadingZeros (preserves at least
// one byte even for value 0). Used as the key for blockInfos/ and as
// the value for blockNumbers/.
//
// Examples:
//
//	0     → [0x00]      (single byte, the "min 7 bytes" rule yields a 1-byte slice for 0)
//	1     → [0x01]
//	256   → [0x01, 0x00]
//	65536 → [0x01, 0x00, 0x00]
func blockNumKeyWithoutLeadingZeros(n uint64) []byte {
	var buf [8]byte
	binary.BigEndian.PutUint64(buf[:], n)

	// Mirrors Math.Min(LeadingZeroCount(value)/8, sizeof(long)-1) — i.e.
	// always keep at least the last byte even when value is zero.
	start := 0
	for start < 7 && buf[start] == 0 {
		start++
	}
	return buf[start:]
}

// writeGenesisBlockToDBs persists a fully-formed genesis header into the
// 7 DBs Nethermind reads on boot.
//
// Layout (per-DB, all writes go to the genesis row at block 0):
//
//	headers/      key = numBE(8)||hash(32)   value = RLP(header)
//	blocks/       key = numBE(8)||hash(32)   value = RLP(block)
//	blockNumbers/ key = hash(32)             value = numBE(8) fixed-width
//	blockInfos/   key = numBE(no leading zeros, ≥1 byte)
//	              value = RLP(ChainLevelInfo{HasBlockOnMainChain=true,
//	                                          BlockInfos=[BlockInfo{
//	                                            BlockHash, WasProcessed=true, TD, Metadata=0
//	                                          }]})
//	receipts/     CF "Blocks": key = numBE(8)||hash(32)  value = 0xc0 (empty list)
//
// State and code DBs are NOT written here — for the empty-alloc case,
// state stays empty (Nethermind short-circuits reads at EmptyTreeHash
// per NodeStorage.cs:107) and code has no entries.
//
// The WasProcessed=true flag in BlockInfos[0] is the boot gate per
// BlockTree.cs:152-171 — without it, Nethermind's LoadGenesisBlock step
// re-runs its own loader and ignores our DB.
func writeGenesisBlockToDBs(dbs *nethDBs, header *types.Header) (common.Hash, error) {
	wo := grocksdb.NewDefaultWriteOptions()
	defer wo.Destroy()

	headerHash := header.Hash() // keccak(rlp(header))
	blockNumber := header.Number.Uint64()

	compositeKey := blockNumPrefixedKey(blockNumber, headerHash)
	numKey := blockNumKeyWithoutLeadingZeros(blockNumber)

	// blockNumbers/ stores the FULL 8-byte big-endian number, not the
	// no-leading-zeros variant. Nethermind's HeaderStore.GetBlockNumberFromBlockNumberDb
	// throws InvalidDataException("Unexpected number span length: ...") if
	// the value is anything other than exactly 8 bytes — see
	// HeaderStore.cs:103. The blockInfos key uses no-leading-zeros, but the
	// blockNumbers value is always 8 bytes.
	var numBE8 [8]byte
	binary.BigEndian.PutUint64(numBE8[:], blockNumber)

	// 1. headers/ — composite key → RLP(header)
	headerRLP, err := nethrlp.EncodeHeader(header)
	if err != nil {
		return common.Hash{}, fmt.Errorf("encode header: %w", err)
	}
	if err := dbs.headers.Put(wo, compositeKey, headerRLP); err != nil {
		return common.Hash{}, fmt.Errorf("write headers/: %w", err)
	}

	// 2. blocks/ — composite key → RLP(block) (header + empty body)
	body := &types.Body{}
	block := types.NewBlockWithHeader(header).WithBody(*body)
	blockRLP, err := nethrlp.EncodeBlock(block)
	if err != nil {
		return common.Hash{}, fmt.Errorf("encode block: %w", err)
	}
	if err := dbs.blocks.Put(wo, compositeKey, blockRLP); err != nil {
		return common.Hash{}, fmt.Errorf("write blocks/: %w", err)
	}

	// 3. blockNumbers/ — hash(32) → numBE(8) fixed
	if err := dbs.blockNumbers.Put(wo, headerHash[:], numBE8[:]); err != nil {
		return common.Hash{}, fmt.Errorf("write blockNumbers/: %w", err)
	}

	// 4. blockInfos/ — numBE → RLP(ChainLevelInfo) with WasProcessed=true.
	//    This is the boot gate.
	td := header.Difficulty
	if td == nil {
		td = new(big.Int)
	}
	cli := &nethrlp.ChainLevelInfo{
		HasBlockOnMainChain: true,
		BlockInfos: []*nethrlp.BlockInfo{
			{
				BlockHash:       [32]byte(headerHash),
				WasProcessed:    true,
				TotalDifficulty: td,
				Metadata:        0, // BlockMetadata.None
			},
		},
	}
	cliRLP := nethrlp.EncodeChainLevelInfo(cli)
	if err := dbs.blockInfos.Put(wo, numKey, cliRLP); err != nil {
		return common.Hash{}, fmt.Errorf("write blockInfos/: %w", err)
	}

	// 5. receipts/Blocks CF — composite key → 0xc0 (empty list).
	//    Nethermind expects an entry to exist even for transaction-free blocks.
	emptyReceipts := nethrlp.EncodeReceipts(nil)
	if err := dbs.receipts.PutCF(wo, dbs.receiptsBlocksCF, compositeKey, emptyReceipts); err != nil {
		return common.Hash{}, fmt.Errorf("write receipts/Blocks: %w", err)
	}

	return headerHash, nil
}

// buildEmptyAllocGenesisHeader constructs a minimal Nethermind-compatible
// genesis header for the empty-alloc case. ChainID and gasLimit come
// from the caller (state-actor's genesis JSON or hardcoded defaults);
// the empty trie/receipts/uncle hashes use go-ethereum's standard
// constants — Nethermind reads identical RLP for the same inputs.
func buildEmptyAllocGenesisHeader(chainID int64, gasLimit uint64, extraData []byte, timestamp uint64) *types.Header {
	_ = chainID // header doesn't carry chainID directly; surfaces via the chainspec on boot
	return &types.Header{
		ParentHash:  common.Hash{},
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    common.Address{},
		Root:        common.Hash(neth.EmptyTreeHash), // empty state trie
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Difficulty:  big.NewInt(0), // post-merge / dev mode
		Number:      big.NewInt(0),
		GasLimit:    gasLimit,
		GasUsed:     0,
		Time:        timestamp,
		Extra:       extraData,
		MixDigest:   common.Hash{},
		Nonce:       types.BlockNonce{},
	}
}
