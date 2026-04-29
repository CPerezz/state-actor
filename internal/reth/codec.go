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
