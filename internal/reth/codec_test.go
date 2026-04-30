package reth

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
)

func TestVarUintRoundtrip(t *testing.T) {
	cases := []uint64{
		0, 1, 0x7f, 0x80, 0xff, 0x100, 0x3fff, 0x4000,
		0xffff, 0x10000, 0xffffffff, 1<<56 - 1, ^uint64(0),
	}
	for _, v := range cases {
		var buf bytes.Buffer
		encodeVarUint(&buf, v)
		decoded, n := decodeVarUint(buf.Bytes())
		if decoded != v {
			t.Errorf("varuint roundtrip: %#x -> hex=%x -> %#x", v, buf.Bytes(), decoded)
		}
		if n != buf.Len() {
			t.Errorf("varuint consumed mismatch for %#x: encoded %d, consumed %d", v, buf.Len(), n)
		}
	}
}

func TestVarUintKnownVectors(t *testing.T) {
	cases := []struct {
		in  uint64
		hex string
	}{
		{0, "00"},
		{1, "01"},
		{0x7f, "7f"},
		{0x80, "8001"},
		{0x3fff, "ff7f"},
		{0x4000, "808001"},
		{0xffffffff, "ffffffff0f"},
	}
	for _, c := range cases {
		var buf bytes.Buffer
		encodeVarUint(&buf, c.in)
		got := bytesToHex(buf.Bytes())
		if got != c.hex {
			t.Errorf("varuint(%#x) = %s, want %s", c.in, got, c.hex)
		}
	}
}

// bytesToHex is a local helper used across codec tests.
func bytesToHex(b []byte) string {
	const hexchars = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, v := range b {
		out[i*2] = hexchars[v>>4]
		out[i*2+1] = hexchars[v&0x0f]
	}
	return string(out)
}

func TestU64StrippedRoundtrip(t *testing.T) {
	cases := []uint64{
		0, 1, 0xff, 0x100, 0x10000, 0xffffffff, 0x100000000, ^uint64(0),
	}
	for _, v := range cases {
		var buf bytes.Buffer
		n := encodeU64Compact(&buf, v)
		if n != buf.Len() {
			t.Errorf("encodeU64Compact(%#x) returned len=%d but wrote %d bytes", v, n, buf.Len())
		}
		decoded := decodeU64Compact(buf.Bytes(), n)
		if decoded != v {
			t.Errorf("u64 stripped roundtrip: %#x -> hex=%x -> %#x", v, buf.Bytes(), decoded)
		}
	}
}

func TestU64StrippedZero(t *testing.T) {
	var buf bytes.Buffer
	n := encodeU64Compact(&buf, 0)
	if n != 0 || buf.Len() != 0 {
		t.Errorf("u64=0 must encode to 0 bytes, got n=%d len=%d hex=%x", n, buf.Len(), buf.Bytes())
	}
}

func TestU64StrippedHighByte(t *testing.T) {
	var buf bytes.Buffer
	n := encodeU64Compact(&buf, 0x12345678)
	got := bytesToHex(buf.Bytes())
	if got != "12345678" {
		t.Errorf("u64(0x12345678) = %s, want 12345678", got)
	}
	if n != 4 {
		t.Errorf("u64(0x12345678) length = %d, want 4", n)
	}
}

func TestU256StrippedRoundtrip(t *testing.T) {
	cases := []*uint256.Int{
		uint256.NewInt(0),
		uint256.NewInt(1),
		uint256.NewInt(0xff),
		uint256.NewInt(0x100),
		new(uint256.Int).SetAllOne(), // 2^256 - 1
		uint256.MustFromHex("0x10000000000000000"),
	}
	for _, v := range cases {
		var buf bytes.Buffer
		n := encodeU256Compact(&buf, v)
		if n != buf.Len() {
			t.Errorf("encodeU256Compact(%s) returned len=%d but wrote %d bytes", v, n, buf.Len())
		}
		decoded := decodeU256Compact(buf.Bytes(), n)
		if !decoded.Eq(v) {
			t.Errorf("U256 stripped roundtrip: %s -> hex=%x -> %s", v, buf.Bytes(), decoded)
		}
	}
}

func TestU256StrippedZero(t *testing.T) {
	var buf bytes.Buffer
	n := encodeU256Compact(&buf, uint256.NewInt(0))
	if n != 0 || buf.Len() != 0 {
		t.Errorf("U256=0 must encode to 0 bytes, got n=%d", n)
	}
}

func TestBytesRoundtrip(t *testing.T) {
	cases := [][]byte{
		nil,
		{},
		{0x00},
		{0x01, 0x02, 0x03},
		make([]byte, 1024),
	}
	cases[len(cases)-1][0] = 0xab
	cases[len(cases)-1][1023] = 0xcd
	for i, in := range cases {
		var buf bytes.Buffer
		n := encodeBytesCompact(&buf, in)
		if n != len(in) {
			t.Errorf("case %d: returned %d, want %d", i, n, len(in))
		}
		out := decodeBytesCompact(buf.Bytes(), n)
		if !bytes.Equal(out, in) {
			t.Errorf("case %d: roundtrip mismatch in=%x out=%x", i, in, out)
		}
	}
}
