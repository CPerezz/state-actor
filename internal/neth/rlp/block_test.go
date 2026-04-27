package rlp

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
)

// TestEncodeReceipts_Empty: the genesis-row receipts entry. Empty list →
// 0xc0. Pinned because it's the byte that ends up at every empty-block
// row in the receipts DB.
func TestEncodeReceipts_Empty(t *testing.T) {
	if got := EncodeReceipts(nil); !bytes.Equal(got, []byte{0xc0}) {
		t.Errorf("nil receipts: got %x, want c0", got)
	}
	if got := EncodeReceipts([][]byte{}); !bytes.Equal(got, []byte{0xc0}) {
		t.Errorf("empty receipts: got %x, want c0", got)
	}
}

// TestEncodeReceipts_NonEmptyPanics: surfacing the scope boundary loudly.
// If state-actor ever ships post-genesis writes, this panic forces the
// implementer to flesh out the receipt-storage-format encoder rather than
// silently producing malformed bytes.
func TestEncodeReceipts_NonEmptyPanics(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("EncodeReceipts did not panic on non-empty input")
		}
	}()
	EncodeReceipts([][]byte{{0x01}})
}

// TestEncodeHeader_RoundTripsViaGeth: the encoder wraps go-ethereum's
// Header RLP, so its output must round-trip through go-ethereum's decoder.
// This test ensures we haven't accidentally introduced a wrapper layer
// that breaks the equivalence — the test would fail if EncodeHeader
// suddenly became a hand-rolled encoder that drifted from go-ethereum.
func TestEncodeHeader_RoundTripsViaGeth(t *testing.T) {
	h := &types.Header{
		ParentHash:  common.Hash{},
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    common.Address{},
		Root:        common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"),
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Difficulty:  big.NewInt(0),
		Number:      big.NewInt(0),
		GasLimit:    30_000_000,
		Time:        0,
		Extra:       []byte("genesis"),
		MixDigest:   common.Hash{},
		Nonce:       types.BlockNonce{},
	}

	encoded, err := EncodeHeader(h)
	if err != nil {
		t.Fatalf("EncodeHeader: %v", err)
	}

	var got types.Header
	if err := decodeHeaderForTest(encoded, &got); err != nil {
		t.Fatalf("round-trip decode: %v", err)
	}

	if got.Hash() != h.Hash() {
		t.Errorf("header hash mismatch: got %s, want %s", got.Hash().Hex(), h.Hash().Hex())
	}
}

// TestEncodeBlock_GenesisShape covers an empty-body genesis block:
// [header, [], []] (no withdrawals on a Berlin-shaped header).
// The outer RLP shape is verified by re-decoding via go-ethereum.
func TestEncodeBlock_GenesisShape(t *testing.T) {
	h := &types.Header{
		ParentHash:  common.Hash{},
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    common.Address{},
		Root:        common.Hash{},
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Difficulty:  big.NewInt(0),
		Number:      big.NewInt(0),
		GasLimit:    30_000_000,
		Time:        0,
	}
	body := &types.Body{} // no txs, uncles, or withdrawals
	block := types.NewBlockWithHeader(h).WithBody(*body)

	encoded, err := EncodeBlock(block)
	if err != nil {
		t.Fatalf("EncodeBlock: %v", err)
	}
	if len(encoded) == 0 {
		t.Fatal("EncodeBlock returned empty bytes")
	}

	var got types.Block
	if err := decodeBlockForTest(encoded, &got); err != nil {
		t.Fatalf("round-trip decode: %v", err)
	}
	if got.Hash() != block.Hash() {
		t.Errorf("block hash mismatch: got %s, want %s", got.Hash().Hex(), block.Hash().Hex())
	}
	if len(got.Transactions()) != 0 || len(got.Uncles()) != 0 {
		t.Errorf("expected empty body, got %d txs / %d uncles", len(got.Transactions()), len(got.Uncles()))
	}
}
