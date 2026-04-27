package rlp

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"
)

// TestEncodeChainLevelInfo_GenesisSingle pins the canonical
// genesis-blockInfos[0] entry: HasBlockOnMainChain=true, exactly one
// BlockInfo (the genesis block, WasProcessed=true).
//
// Layout:
//
//	HasBlockOnMainChain: true                            → 0x01     (1 byte)
//	[ BlockInfo (35-byte content + 0xe3 header = 36) ]   → 0xe3 + 36 = 37 bytes
//	  Wrapped in inner list: 0xc0 + 36 = 0xe4 + 36       → 37 bytes
//	Outer content: 1 + 37 = 38 → outer header 0xc0 + 38 = 0xe6.
func TestEncodeChainLevelInfo_GenesisSingle(t *testing.T) {
	hash := genesisHashFixture()
	bi := &BlockInfo{
		BlockHash:       hash,
		WasProcessed:    true,
		TotalDifficulty: big.NewInt(0),
	}

	got := EncodeChainLevelInfo(&ChainLevelInfo{
		HasBlockOnMainChain: true,
		BlockInfos:          []*BlockInfo{bi},
	})

	// blockInfo bytes: 0xe3 0xa0 <hash> 0x01 0x80 = 36 bytes
	// inner list of 1 blockInfo: 0xc0+36 = 0xe4 + 36 bytes = 37 bytes
	// outer content: 1 (HasBlockOnMainChain=true → 0x01) + 37 = 38
	// outer header: 0xc0+38 = 0xe6
	innerBI := EncodeBlockInfo(bi)
	want := append([]byte{0xe6, 0x01, 0xe4}, innerBI...)
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeChainLevelInfo genesis:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeChainLevelInfo_TwoBlockInfos covers a fork: two BlockInfos at
// the same height. Inner list grows; outer list header recomputes.
func TestEncodeChainLevelInfo_TwoBlockInfos(t *testing.T) {
	var h1, h2 [32]byte
	for i := range h1 {
		h1[i] = 0x11
		h2[i] = 0x22
	}

	bi1 := &BlockInfo{BlockHash: h1, WasProcessed: true, TotalDifficulty: big.NewInt(0)}
	bi2 := &BlockInfo{BlockHash: h2, WasProcessed: false, TotalDifficulty: big.NewInt(0)}

	got := EncodeChainLevelInfo(&ChainLevelInfo{
		HasBlockOnMainChain: false,
		BlockInfos:          []*BlockInfo{bi1, bi2},
	})

	// Each BlockInfo encodes to 36 bytes total: 35 bytes content (33 hash
	// + 1 wasProc + 1 td-zero) + 1 byte 0xe3 list header.
	// 2 BlockInfos = 72 bytes content of the inner list.
	// Inner list header for 72 bytes: 0xf8 0x48 (2 bytes); inner list = 74 bytes.
	// Outer content: 1 (false bool) + 74 (inner list incl. header) = 75.
	// Outer list header for 75 bytes: 0xf8 0x4b.
	if got[0] != 0xf8 || got[1] != 0x4b {
		t.Fatalf("expected outer header 0xf8 0x4b, got 0x%02x 0x%02x", got[0], got[1])
	}
	if got[2] != 0x80 {
		t.Errorf("HasBlockOnMainChain=false should encode as 0x80, got 0x%02x", got[2])
	}
	// Inner list header at offset 3:
	if got[3] != 0xf8 || got[4] != 0x48 {
		t.Errorf("expected inner list header 0xf8 0x48, got 0x%02x 0x%02x", got[3], got[4])
	}
}

// TestEncodeChainLevelInfo_EmptyBlockInfos: empty inner list.
func TestEncodeChainLevelInfo_EmptyBlockInfos(t *testing.T) {
	got := EncodeChainLevelInfo(&ChainLevelInfo{
		HasBlockOnMainChain: true,
		BlockInfos:          nil,
	})

	// Outer: [0x01, 0xc0] = 2 bytes content. Header 0xc0+2 = 0xc2.
	want, _ := hex.DecodeString("c2" + "01" + "c0")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeChainLevelInfo empty:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeChainLevelInfo_Nil: nil → empty list 0xc0.
func TestEncodeChainLevelInfo_Nil(t *testing.T) {
	got := EncodeChainLevelInfo(nil)
	if !bytes.Equal(got, []byte{0xc0}) {
		t.Fatalf("EncodeChainLevelInfo nil: got %x, want c0", got)
	}
}

// TestEncodeChainLevelInfo_PanicsOnNilEntry: nil BlockInfo inside the slice
// is a programmer error — Nethermind throws a TypedException for it; we
// panic in the same way.
func TestEncodeChainLevelInfo_PanicsOnNilEntry(t *testing.T) {
	defer func() {
		if r := recover(); r == nil {
			t.Error("expected panic on nil BlockInfo in slice")
		}
	}()
	EncodeChainLevelInfo(&ChainLevelInfo{
		HasBlockOnMainChain: true,
		BlockInfos:          []*BlockInfo{nil},
	})
}
