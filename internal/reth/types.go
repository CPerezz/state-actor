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
