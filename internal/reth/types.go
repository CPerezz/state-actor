package reth

import (
	"bytes"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// Account mirrors reth-db-models 0.3.1's Account struct.
//
// Field order is load-bearing: Compact encoding writes fields in declaration
// order, and the bitflag header records per-field metadata in this same order.
//
// Wire format (per reth-codecs 0.3.1 + derive 0.3.1):
//  1. 2-byte bitflag header: nonce(4) | balance(6) | bytecode_hash_present(1) padding=5
//  2. Stripped-be nonce: 0..=8 bytes (length from header)
//  3. Stripped-be balance: 0..=32 bytes (length from header)
//  4. If bytecode_hash present: 32-byte B256
type Account struct {
	Nonce        uint64
	Balance      *uint256.Int // never nil; zero value is uint256.NewInt(0)
	BytecodeHash *common.Hash // nil = EOA; Some = contract
}

// EncodeCompact appends the Compact wire form of a to buf and returns the
// total bytes written.
func (a *Account) EncodeCompact(buf *bytes.Buffer) int {
	// Pre-encode the variable-length fields to determine their byte counts.
	var nonceBuf, balBuf bytes.Buffer
	nonceN := encodeU64Compact(&nonceBuf, a.Nonce)
	balN := encodeU256Compact(&balBuf, a.Balance)

	// Build bitflag header (LSB-first, fields in struct order).
	var bb bitflagBuilder
	bb.PutU64Length(nonceN)
	bb.PutU256Length(balN)
	bb.PutBool(a.BytecodeHash != nil)
	header := bb.Finalize(11)

	// Emit header, fields, then optional bytecode hash.
	written := 0
	written += copy(bufWrite(buf, len(header)), header)
	written += copy(bufWrite(buf, nonceN), nonceBuf.Bytes())
	written += copy(bufWrite(buf, balN), balBuf.Bytes())
	if a.BytecodeHash != nil {
		written += copy(bufWrite(buf, 32), a.BytecodeHash[:])
	}
	return written
}

// DecodeCompact reads the Compact wire form into a from b. Returns the number
// of bytes consumed.
func (a *Account) DecodeCompact(b []byte, totalLen int) int {
	if len(b) < 2 {
		panic("Account: header truncated")
	}
	header := b[:2]
	cursor := 2

	var br bitflagReader
	br.Init(header, 11)
	nonceN := br.GetU64Length()
	balN := br.GetU256Length()
	hasCode := br.GetBool()

	a.Nonce = decodeU64Compact(b[cursor:], nonceN)
	cursor += nonceN

	a.Balance = decodeU256Compact(b[cursor:], balN)
	cursor += balN

	if hasCode {
		var h common.Hash
		copy(h[:], b[cursor:cursor+32])
		a.BytecodeHash = &h
		cursor += 32
	} else {
		a.BytecodeHash = nil
	}

	if cursor != totalLen {
		panic("Account: cursor != totalLen — codec bug")
	}
	return cursor
}

// bufWrite extends buf by n bytes and returns a slice into the new region for
// `copy` to fill. Useful for shaping struct emission code as a sequence of
// `copy(bufWrite(...), src)` calls that mirror the wire layout one-to-one.
func bufWrite(buf *bytes.Buffer, n int) []byte {
	start := buf.Len()
	buf.Write(make([]byte, n))
	return buf.Bytes()[start : start+n]
}

// StorageEntry mirrors reth-db-models 0.3.1's StorageEntry struct.
//
// Wire format:
//  1. 1-byte bitflag header: value_length(6) padding=2
//  2. 32-byte key (fixed, no compaction)
//  3. Stripped-be value: 0..=32 bytes
type StorageEntry struct {
	Key   common.Hash
	Value *uint256.Int
}

func (s *StorageEntry) EncodeCompact(buf *bytes.Buffer) int {
	var valBuf bytes.Buffer
	valN := encodeU256Compact(&valBuf, s.Value)

	var bb bitflagBuilder
	bb.PutU256Length(valN)
	header := bb.Finalize(6)

	written := 0
	written += copy(bufWrite(buf, len(header)), header)
	written += copy(bufWrite(buf, 32), s.Key[:])
	written += copy(bufWrite(buf, valN), valBuf.Bytes())
	return written
}

func (s *StorageEntry) DecodeCompact(b []byte, totalLen int) int {
	if len(b) < 1 {
		panic("StorageEntry: header truncated")
	}
	header := b[:1]
	cursor := 1

	var br bitflagReader
	br.Init(header, 6)
	valN := br.GetU256Length()

	copy(s.Key[:], b[cursor:cursor+32])
	cursor += 32

	s.Value = decodeU256Compact(b[cursor:], valN)
	cursor += valN

	if cursor != totalLen {
		panic("StorageEntry: cursor != totalLen")
	}
	return cursor
}

// EncodeIntegerList writes a Vec<u64> in Compact form: varuint(count) followed
// by per-element varuint(value). Used by AccountsHistory / StoragesHistory.
func EncodeIntegerList(buf *bytes.Buffer, list []uint64) {
	encodeVarUint(buf, uint64(len(list)))
	for _, v := range list {
		encodeVarUint(buf, v)
	}
}

// DecodeIntegerList reads a Vec<u64> from b. Returns the slice and bytes consumed.
func DecodeIntegerList(b []byte) ([]uint64, int) {
	count, n := decodeVarUint(b)
	out := make([]uint64, count)
	for i := uint64(0); i < count; i++ {
		v, m := decodeVarUint(b[n:])
		out[i] = v
		n += m
	}
	return out, n
}

// ShardedKeyAddress is the AccountsHistory key. Address followed by a u64
// block-number suffix.
//
// Wire (MDBX key encoding, fixed 28 bytes):
//
//	address[20] || BE_u64(block_number)[8]
type ShardedKeyAddress struct {
	Address     common.Address
	BlockNumber uint64
}

func (k *ShardedKeyAddress) EncodeKey(buf *bytes.Buffer) {
	buf.Write(k.Address[:])
	writeBEU64(buf, k.BlockNumber)
}

func (k *ShardedKeyAddress) DecodeKey(b []byte) {
	if len(b) < 28 {
		panic("ShardedKeyAddress: truncated key")
	}
	copy(k.Address[:], b[:20])
	k.BlockNumber = readBEU64(b[20:])
}

// StorageShardedKey is the StoragesHistory key. Address + per-slot storage
// key + block-number suffix.
//
// Wire (MDBX key encoding, fixed 60 bytes):
//
//	address[20] || storage_key[32] || BE_u64(block_number)[8]
type StorageShardedKey struct {
	Address     common.Address
	StorageKey  common.Hash
	BlockNumber uint64
}

func (k *StorageShardedKey) EncodeKey(buf *bytes.Buffer) {
	buf.Write(k.Address[:])
	buf.Write(k.StorageKey[:])
	writeBEU64(buf, k.BlockNumber)
}

func (k *StorageShardedKey) DecodeKey(b []byte) {
	if len(b) < 60 {
		panic("StorageShardedKey: truncated key")
	}
	copy(k.Address[:], b[:20])
	copy(k.StorageKey[:], b[20:52])
	k.BlockNumber = readBEU64(b[52:])
}

// BlockNumberAddress is the StorageChangeSets key. Block-number FIRST so
// MDBX sorts numerically by block.
//
// Wire (MDBX key encoding, fixed 28 bytes):
//
//	BE_u64(block_number)[8] || address[20]
type BlockNumberAddress struct {
	BlockNumber uint64
	Address     common.Address
}

func (k *BlockNumberAddress) EncodeKey(buf *bytes.Buffer) {
	writeBEU64(buf, k.BlockNumber)
	buf.Write(k.Address[:])
}

func (k *BlockNumberAddress) DecodeKey(b []byte) {
	if len(b) < 28 {
		panic("BlockNumberAddress: truncated key")
	}
	k.BlockNumber = readBEU64(b[:8])
	copy(k.Address[:], b[8:28])
}

// writeBEU64 writes v as 8 big-endian bytes.
func writeBEU64(buf *bytes.Buffer, v uint64) {
	var be [8]byte
	be[0] = byte(v >> 56)
	be[1] = byte(v >> 48)
	be[2] = byte(v >> 40)
	be[3] = byte(v >> 32)
	be[4] = byte(v >> 24)
	be[5] = byte(v >> 16)
	be[6] = byte(v >> 8)
	be[7] = byte(v)
	buf.Write(be[:])
}

// readBEU64 reads 8 big-endian bytes from b[:8] as uint64.
func readBEU64(b []byte) uint64 {
	return uint64(b[0])<<56 |
		uint64(b[1])<<48 |
		uint64(b[2])<<40 |
		uint64(b[3])<<32 |
		uint64(b[4])<<24 |
		uint64(b[5])<<16 |
		uint64(b[6])<<8 |
		uint64(b[7])
}
