package rlp

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// TestEncodeStorageValue_Zero verifies that a zero slot (all bytes 0x00)
// encodes to exactly 0x80 — the RLP null byte.
//
// Source: PathBasedWorldView.java:53-57 — encodeTrieValue(bytes) writes
// bytes.trimLeadingZeros() through RLP. For UInt256(0), trimmed = empty → 0x80.
func TestEncodeStorageValue_Zero(t *testing.T) {
	got := EncodeStorageValue(common.Hash{}) // all-zero hash
	want := []byte{0x80}
	if !bytes.Equal(got, want) {
		t.Fatalf("zero slot: got %x, want %x", got, want)
	}
}

// TestEncodeStorageValue_One verifies that the value 1 (right-padded with
// zeros in a common.Hash) encodes to RLP(0x01) = a single byte 0x01.
// (RLP single-byte scalars in range [0x00, 0x7f] are self-encoded.)
func TestEncodeStorageValue_One(t *testing.T) {
	var h common.Hash
	h[31] = 0x01 // big-endian 1
	got := EncodeStorageValue(h)
	want := []byte{0x01}
	if !bytes.Equal(got, want) {
		t.Fatalf("value 1: got %x, want %x", got, want)
	}
}

// TestEncodeStorageValue_SingleByte verifies trimming: only the non-zero byte
// is kept; it encodes as RLP string of length 1 (0x81 prefix for byte > 0x7f).
func TestEncodeStorageValue_SingleByte(t *testing.T) {
	var h common.Hash
	h[31] = 0xff // big-endian 255
	got := EncodeStorageValue(h)
	want := []byte{0x81, 0xff}
	if !bytes.Equal(got, want) {
		t.Fatalf("value 0xff: got %x, want %x", got, want)
	}
}

// TestEncodeStorageValue_MultiByteValue covers a value that requires multiple
// bytes after trimming leading zeros.
func TestEncodeStorageValue_MultiByteValue(t *testing.T) {
	// Value = 0x0102 (big-endian in 32-byte hash)
	h, _ := hex.DecodeString(
		"0000000000000000000000000000000000000000000000000000000000000102",
	)
	var hash common.Hash
	copy(hash[:], h)
	got := EncodeStorageValue(hash)
	// trimmed = [0x01, 0x02] (2 bytes), RLP = 0x82 ++ 0x01 ++ 0x02
	want := []byte{0x82, 0x01, 0x02}
	if !bytes.Equal(got, want) {
		t.Fatalf("value 0x0102: got %x, want %x", got, want)
	}
}

// TestEncodeStorageValue_MaxU256 verifies a full 32-byte non-zero value.
// All-0xff has no leading zeros to trim; the full 32 bytes are encoded.
func TestEncodeStorageValue_MaxU256(t *testing.T) {
	var h common.Hash
	for i := range h {
		h[i] = 0xff
	}
	got := EncodeStorageValue(h)
	// 32-byte string: 0xa0 (= 0x80 + 32) ++ 32×0xff
	if len(got) != 33 {
		t.Fatalf("max u256 length: got %d, want 33", len(got))
	}
	if got[0] != 0xa0 {
		t.Fatalf("max u256 prefix: got %x, want 0xa0", got[0])
	}
	for i, b := range got[1:] {
		if b != 0xff {
			t.Fatalf("max u256 byte[%d]: got %x, want 0xff", i+1, b)
		}
	}
}
