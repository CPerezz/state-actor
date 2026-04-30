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

func TestStorageEntryRoundtrip(t *testing.T) {
	cases := []StorageEntry{
		{Key: common.HexToHash("0x00"), Value: uint256.NewInt(0)},
		{Key: common.HexToHash("0x01"), Value: uint256.NewInt(0xff)},
		{Key: common.HexToHash("0xdeadbeef"), Value: new(uint256.Int).SetAllOne()},
	}
	for i, in := range cases {
		var buf bytes.Buffer
		n := in.EncodeCompact(&buf)
		var out StorageEntry
		consumed := out.DecodeCompact(buf.Bytes(), n)
		if consumed != n {
			t.Errorf("case %d: consumed %d, encoded %d", i, consumed, n)
		}
		if in.Key != out.Key || !in.Value.Eq(out.Value) {
			t.Errorf("case %d: in=%+v out=%+v hex=%x", i, in, out, buf.Bytes())
		}
	}
}

func TestIntegerListRoundtrip(t *testing.T) {
	cases := [][]uint64{
		nil,
		{},
		{0},
		{0, 1, 2, 3},
		{0, 100, 200, 0x12345678},
	}
	for i, in := range cases {
		var buf bytes.Buffer
		EncodeIntegerList(&buf, in)
		out, n := DecodeIntegerList(buf.Bytes())
		if n != buf.Len() {
			t.Errorf("case %d: consumed %d, encoded %d", i, n, buf.Len())
		}
		if (len(in) == 0 && len(out) != 0) || (len(in) > 0 && !uint64SliceEqual(in, out)) {
			t.Errorf("case %d: in=%v out=%v hex=%x", i, in, out, buf.Bytes())
		}
	}
}

func uint64SliceEqual(a, b []uint64) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

func TestShardedKeyAddressRoundtrip(t *testing.T) {
	addr := common.HexToAddress("0xdeadbeef00000000000000000000000000000000")
	key := ShardedKeyAddress{Address: addr, BlockNumber: ^uint64(0)}
	var buf bytes.Buffer
	key.EncodeKey(&buf)
	if buf.Len() != 28 {
		t.Fatalf("ShardedKeyAddress encodes to %d bytes, want 28", buf.Len())
	}
	var out ShardedKeyAddress
	out.DecodeKey(buf.Bytes())
	if out != key {
		t.Errorf("ShardedKeyAddress roundtrip: %+v -> %+v hex=%x", key, out, buf.Bytes())
	}
}

func TestStorageShardedKeyRoundtrip(t *testing.T) {
	addr := common.HexToAddress("0xff")
	slot := common.HexToHash("0x42")
	key := StorageShardedKey{Address: addr, StorageKey: slot, BlockNumber: ^uint64(0)}
	var buf bytes.Buffer
	key.EncodeKey(&buf)
	if buf.Len() != 60 {
		t.Fatalf("StorageShardedKey encodes to %d bytes, want 60", buf.Len())
	}
	var out StorageShardedKey
	out.DecodeKey(buf.Bytes())
	if out != key {
		t.Errorf("StorageShardedKey roundtrip mismatch hex=%x", buf.Bytes())
	}
}

func TestBlockNumberAddressRoundtrip(t *testing.T) {
	addr := common.HexToAddress("0x42")
	key := BlockNumberAddress{BlockNumber: 0x1234567890ab, Address: addr}
	var buf bytes.Buffer
	key.EncodeKey(&buf)
	if buf.Len() != 28 {
		t.Fatalf("BlockNumberAddress encodes to %d bytes, want 28", buf.Len())
	}
	// Sanity: high block bytes come first (BE) so MDBX sorts numerically by block.
	if buf.Bytes()[0] != 0x00 || buf.Bytes()[7] != 0xab {
		t.Errorf("BlockNumberAddress not big-endian: hex=%x", buf.Bytes())
	}
	var out BlockNumberAddress
	out.DecodeKey(buf.Bytes())
	if out != key {
		t.Errorf("BlockNumberAddress roundtrip mismatch")
	}
}
