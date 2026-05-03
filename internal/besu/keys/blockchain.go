package keys

import "github.com/ethereum/go-ethereum/common"

// BLOCKCHAIN CF key constructors.
//
// All keys are: prefix_byte(1B) ++ payload.
// Block-hash-keyed: payload = blockHash (32B).
// Block-number-keyed: payload = UInt256(blockNum) (32B big-endian, zero-padded).
//
// Citation: KeyValueStoragePrefixedKeyBlockchainStorage.java:64-70,165-167
// (besu tag 26.5.0).

// Prefix bytes for BLOCKCHAIN CF keys.
// Source: KeyValueStoragePrefixedKeyBlockchainStorage.java:64-70.
const (
	blockHeaderPrefix     byte = 0x02
	blockBodyPrefix       byte = 0x03
	blockReceiptsPrefix   byte = 0x04
	canonicalHashPrefix   byte = 0x05
	totalDifficultyPrefix byte = 0x06
)

// BlockHeaderKey returns the BLOCKCHAIN CF key for a block's RLP header.
// Layout: 0x02 ++ blockHash(32B). Total: 33 bytes.
func BlockHeaderKey(blockHash common.Hash) []byte {
	return prefixedHashKey(blockHeaderPrefix, blockHash)
}

// BlockBodyKey returns the BLOCKCHAIN CF key for a block's RLP body.
// Layout: 0x03 ++ blockHash(32B). Total: 33 bytes.
func BlockBodyKey(blockHash common.Hash) []byte {
	return prefixedHashKey(blockBodyPrefix, blockHash)
}

// BlockReceiptsKey returns the BLOCKCHAIN CF key for a block's RLP receipts.
// Layout: 0x04 ++ blockHash(32B). Total: 33 bytes.
func BlockReceiptsKey(blockHash common.Hash) []byte {
	return prefixedHashKey(blockReceiptsPrefix, blockHash)
}

// CanonicalHashKey returns the BLOCKCHAIN CF key for the canonical block
// number → hash index.
//
// Layout: 0x05 ++ UInt256(blockNum)(32B). Total: 33 bytes.
//
// UInt256 encoding: 32-byte big-endian zero-padded representation, matching
// Besu's UInt256.valueOf().toBytes32(). NOT variable-length: block 0 → 32
// zero bytes; block 1 → 31 zeros + 0x01.
func CanonicalHashKey(blockNum uint64) []byte {
	out := make([]byte, 33)
	out[0] = canonicalHashPrefix
	// Write blockNum as 32-byte big-endian in out[1..33].
	// uint64 occupies the last 8 bytes (positions 25..32 in the 32B field).
	out[25] = byte(blockNum >> 56)
	out[26] = byte(blockNum >> 48)
	out[27] = byte(blockNum >> 40)
	out[28] = byte(blockNum >> 32)
	out[29] = byte(blockNum >> 24)
	out[30] = byte(blockNum >> 16)
	out[31] = byte(blockNum >> 8)
	out[32] = byte(blockNum)
	return out
}

// TotalDifficultyKey returns the BLOCKCHAIN CF key for a block's total
// difficulty (stored as Bytes32).
// Layout: 0x06 ++ blockHash(32B). Total: 33 bytes.
func TotalDifficultyKey(blockHash common.Hash) []byte {
	return prefixedHashKey(totalDifficultyPrefix, blockHash)
}

// prefixedHashKey builds a 33-byte key: prefix(1B) ++ hash(32B).
func prefixedHashKey(prefix byte, hash common.Hash) []byte {
	out := make([]byte, 33)
	out[0] = prefix
	copy(out[1:], hash[:])
	return out
}
