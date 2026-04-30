package reth

import (
	"bytes"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

func TestAccountRoundtrip(t *testing.T) {
	hash := common.HexToHash("0xc5d2460186f7233c927e7db2dcc703c0e500b653ca82273b7bfad8045d85a470")
	cases := []Account{
		{Nonce: 0, Balance: uint256.NewInt(0), BytecodeHash: nil},
		{Nonce: 1, Balance: uint256.NewInt(0xff), BytecodeHash: nil},
		{Nonce: 0x12345678, Balance: uint256.MustFromHex("0xabcdef0123456789"), BytecodeHash: &hash},
		{Nonce: ^uint64(0), Balance: new(uint256.Int).SetAllOne(), BytecodeHash: &hash},
	}
	for i, in := range cases {
		var buf bytes.Buffer
		n := in.EncodeCompact(&buf)
		if n != buf.Len() {
			t.Errorf("case %d: returned %d, wrote %d", i, n, buf.Len())
		}
		var out Account
		consumed := out.DecodeCompact(buf.Bytes(), n)
		if consumed != n {
			t.Errorf("case %d: consumed %d, encoded %d", i, consumed, n)
		}
		if !accountEqual(in, out) {
			t.Errorf("case %d: in=%+v out=%+v hex=%x", i, in, out, buf.Bytes())
		}
	}
}

func accountEqual(a, b Account) bool {
	if a.Nonce != b.Nonce {
		return false
	}
	if !a.Balance.Eq(b.Balance) {
		return false
	}
	if (a.BytecodeHash == nil) != (b.BytecodeHash == nil) {
		return false
	}
	if a.BytecodeHash != nil && *a.BytecodeHash != *b.BytecodeHash {
		return false
	}
	return true
}
