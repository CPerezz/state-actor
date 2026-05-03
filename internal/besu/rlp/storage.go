package rlp

import (
	"github.com/ethereum/go-ethereum/common"
	gethrlp "github.com/ethereum/go-ethereum/rlp"
)

// EncodeStorageValue encodes a 32-byte storage slot value for the Besu Bonsai
// ACCOUNT_STORAGE_STORAGE column family.
//
// Algorithm (mirrors PathBasedWorldView.encodeTrieValue at PathBasedWorldView.java:53-57):
//  1. Trim leading zero bytes from the big-endian 32-byte representation.
//  2. RLP-encode the trimmed byte slice as a byte string.
//
// Edge cases:
//   - Zero value (all 32 bytes = 0x00): trimmed = []byte{}, RLP([]) = 0x80.
//   - Single non-zero byte x where x <= 0x7f: RLP = byte x itself (self-encoded).
//   - Single non-zero byte x where x > 0x7f: RLP = 0x81 ++ x.
//   - N-byte value (N > 1): RLP = (0x80+N) ++ bytes.
func EncodeStorageValue(value common.Hash) []byte {
	// Trim leading zeros from the 32-byte big-endian representation.
	raw := value[:]
	start := 0
	for start < len(raw) && raw[start] == 0x00 {
		start++
	}
	trimmed := raw[start:] // may be empty (zero slot)

	// gethrlp.EncodeToBytes on a []byte encodes as an RLP byte string.
	// Empty byte slice → 0x80 (RLP null), matching PathBasedWorldView behaviour.
	encoded, err := gethrlp.EncodeToBytes(trimmed)
	if err != nil {
		// gethrlp.EncodeToBytes on a []byte never returns an error.
		panic("besu/rlp.EncodeStorageValue: unexpected RLP error: " + err.Error())
	}
	return encoded
}
