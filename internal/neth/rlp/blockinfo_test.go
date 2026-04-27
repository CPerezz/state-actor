package rlp

import (
	"bytes"
	"encoding/hex"
	"math/big"
	"testing"
)

// genesisHashFixture returns a known 32-byte hash for tests.
func genesisHashFixture() [32]byte {
	var h [32]byte
	for i := range h {
		h[i] = byte(0xa0 + i%16)
	}
	return h
}

// TestEncodeBlockInfo_Genesis covers the canonical genesis-block BlockInfo:
// hash + WasProcessed=true + TotalDifficulty=0, no metadata.
//
// Layout:
//
//	BlockHash:       0xa0 || 32 bytes              = 33 bytes
//	WasProcessed:    true → 0x01                   = 1 byte
//	TotalDifficulty: 0 → 0x80 (empty byte string)  = 1 byte
//	Inner: 35 → list header 0xc0 + 35 = 0xe3.
func TestEncodeBlockInfo_Genesis(t *testing.T) {
	hash := genesisHashFixture()

	got := EncodeBlockInfo(&BlockInfo{
		BlockHash:       hash,
		WasProcessed:    true,
		TotalDifficulty: big.NewInt(0),
	})

	want, _ := hex.DecodeString("e3" + "a0" + hex.EncodeToString(hash[:]) + "01" + "80")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeBlockInfo genesis:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeBlockInfo_NotProcessed pins the wire form of WasProcessed=false.
// In RLP, false encodes as the empty byte string (0x80), NOT as 0x00.
func TestEncodeBlockInfo_NotProcessed(t *testing.T) {
	hash := genesisHashFixture()

	got := EncodeBlockInfo(&BlockInfo{
		BlockHash:       hash,
		WasProcessed:    false,
		TotalDifficulty: big.NewInt(0),
	})

	// Same length as Genesis test (35 byte content) but byte at offset 34
	// is 0x80 instead of 0x01.
	want, _ := hex.DecodeString("e3" + "a0" + hex.EncodeToString(hash[:]) + "80" + "80")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeBlockInfo not-processed:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeBlockInfo_WithMetadata: metadata != 0 must add a 4th element.
// We use Metadata=1 — tests that a nonzero value triggers the extra slot.
func TestEncodeBlockInfo_WithMetadata(t *testing.T) {
	hash := genesisHashFixture()

	got := EncodeBlockInfo(&BlockInfo{
		BlockHash:       hash,
		WasProcessed:    true,
		TotalDifficulty: big.NewInt(0),
		Metadata:        1,
	})

	// Inner: 33 + 1 + 1 + 1 = 36 → list header 0xe4.
	want, _ := hex.DecodeString("e4" + "a0" + hex.EncodeToString(hash[:]) + "01" + "80" + "01")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeBlockInfo with-metadata:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeBlockInfo_NilTotalDifficulty: nil TD must encode as zero
// (empty byte string), not as a panic or extra error path.
func TestEncodeBlockInfo_NilTotalDifficulty(t *testing.T) {
	hash := genesisHashFixture()
	got := EncodeBlockInfo(&BlockInfo{BlockHash: hash, TotalDifficulty: nil})

	// WasProcessed=false → 0x80; TD=nil → treated as 0 → 0x80.
	want, _ := hex.DecodeString("e3" + "a0" + hex.EncodeToString(hash[:]) + "80" + "80")
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeBlockInfo nil TD:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestEncodeBlockInfo_Nil: nil BlockInfo encodes as the empty list 0xc0.
// Nethermind uses this when persisting a deleted entry.
func TestEncodeBlockInfo_Nil(t *testing.T) {
	got := EncodeBlockInfo(nil)
	if !bytes.Equal(got, []byte{0xc0}) {
		t.Fatalf("EncodeBlockInfo nil: got %x, want c0", got)
	}
}

// TestEncodeBlockInfo_LargeTotalDifficulty: a multi-byte TD must be
// length-prefixed correctly. Use 2^64 (9 bytes when serialized).
func TestEncodeBlockInfo_LargeTotalDifficulty(t *testing.T) {
	hash := genesisHashFixture()
	td := new(big.Int).Lsh(big.NewInt(1), 64) // 2^64

	got := EncodeBlockInfo(&BlockInfo{
		BlockHash:       hash,
		WasProcessed:    true,
		TotalDifficulty: td,
	})

	// 2^64 = 0x010000000000000000 (9 bytes). RLP: 0x89 + 9 bytes = 10 bytes.
	// Inner: 33 + 1 + 10 = 44 → list header 0xec.
	want, _ := hex.DecodeString(
		"ec" + "a0" + hex.EncodeToString(hash[:]) +
			"01" +
			"89" + "010000000000000000",
	)
	if !bytes.Equal(got, want) {
		t.Fatalf("EncodeBlockInfo large TD:\n  got:  %x\n  want: %x", got, want)
	}
}
