package reth

import (
	"bytes"
)

// encodeVarUint writes v in LEB128 form (7-bit chunks, MSB continuation).
// Mirrors reth-codecs 0.3.1's varuint encoding for Vec/Option lengths.
func encodeVarUint(buf *bytes.Buffer, v uint64) {
	for v >= 0x80 {
		buf.WriteByte(byte(v) | 0x80)
		v >>= 7
	}
	buf.WriteByte(byte(v))
}

// decodeVarUint reads a LEB128 varuint from b. Returns the decoded value and
// the number of bytes consumed. Panics if b is too short or the encoding
// exceeds 10 bytes (would overflow uint64).
func decodeVarUint(b []byte) (uint64, int) {
	var v uint64
	var shift uint
	for i, c := range b {
		if i >= 10 {
			panic("varuint exceeds 10 bytes")
		}
		v |= uint64(c&0x7f) << shift
		if c&0x80 == 0 {
			return v, i + 1
		}
		shift += 7
	}
	panic("varuint truncated")
}

// encodeU64Compact writes v in big-endian form with leading zero bytes stripped.
// Returns the byte count written. Mirrors reth-codecs 0.3.1's Compact impl
// for u64 (lib.rs:149-183 impl_uint_compact!).
//
// The byte count must be recorded out-of-band (in the parent struct's
// bitflag header for fields; passed to decodeU64Compact by the caller).
func encodeU64Compact(buf *bytes.Buffer, v uint64) int {
	if v == 0 {
		return 0
	}
	var raw [8]byte
	raw[0] = byte(v >> 56)
	raw[1] = byte(v >> 48)
	raw[2] = byte(v >> 40)
	raw[3] = byte(v >> 32)
	raw[4] = byte(v >> 24)
	raw[5] = byte(v >> 16)
	raw[6] = byte(v >> 8)
	raw[7] = byte(v)
	i := 0
	for i < 8 && raw[i] == 0 {
		i++
	}
	n := 8 - i
	buf.Write(raw[i:])
	return n
}

// decodeU64Compact reads a stripped-big-endian u64 of length n bytes from b.
// Length must be 0..=8. Returns the decoded value.
func decodeU64Compact(b []byte, n int) uint64 {
	if n < 0 || n > 8 {
		panic("u64 compact length out of range")
	}
	if n > len(b) {
		panic("u64 compact buffer too short")
	}
	var v uint64
	for i := 0; i < n; i++ {
		v = (v << 8) | uint64(b[i])
	}
	return v
}
