package reth

import (
	"bytes"
	"testing"
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
