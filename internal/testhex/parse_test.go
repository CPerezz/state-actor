package testhex

import (
	"bytes"
	"math/big"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

func TestUint64(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want uint64
	}{
		{"empty", "", 0},
		{"bare_prefix", "0x", 0},
		{"capital_prefix", "0X10", 16},
		{"leading_zero", "0x0102030405060708", 0x0102030405060708},
		{"max_uint64", "0xffffffffffffffff", ^uint64(0)},
		{"no_prefix", "abc", 0xabc},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Uint64(t, "f", tc.in)
			if got != tc.want {
				t.Errorf("Uint64(%q) = %d, want %d", tc.in, got, tc.want)
			}
		})
	}
}

func TestBig(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{"empty", "", "0"},
		{"bare_prefix", "0x", "0"},
		{"leading_zero", "0x00ff", "255"},
		{"past_uint64", "0x10000000000000000", "18446744073709551616"},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Big(t, "f", tc.in)
			want, _ := new(big.Int).SetString(tc.want, 10)
			if got.Cmp(want) != 0 {
				t.Errorf("Big(%q) = %s, want %s", tc.in, got.String(), want.String())
			}
		})
	}
}

func TestBytes(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want []byte
	}{
		{"empty", "", nil},
		{"bare_prefix", "0x", nil},
		{"single_byte", "0xab", []byte{0xab}},
		{"odd_length_padded", "0xa", []byte{0x0a}},
		{"single_zero", "0x0", []byte{0x00}},
		{"long", "0xdeadbeef", []byte{0xde, 0xad, 0xbe, 0xef}},
	}
	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			got := Bytes(t, "f", tc.in)
			if !bytes.Equal(got, tc.want) {
				t.Errorf("Bytes(%q) = %x, want %x", tc.in, got, tc.want)
			}
		})
	}
}

func TestHash(t *testing.T) {
	full := "0x" + "deadbeef" + "00000000" + "00000000" + "00000000" + "00000000" + "00000000" + "00000000" + "deadbeef"
	want := common.HexToHash(full)
	if got := Hash(t, "f", full); got != want {
		t.Errorf("Hash(full) = %s, want %s", got.Hex(), want.Hex())
	}
	if got := Hash(t, "f", ""); got != (common.Hash{}) {
		t.Errorf("Hash(empty) = %s, want zero", got.Hex())
	}
	if got := Hash(t, "f", "0x"); got != (common.Hash{}) {
		t.Errorf("Hash(0x) = %s, want zero", got.Hex())
	}
}

func TestAddress(t *testing.T) {
	addrHex := "0x1234567890abcdef1234567890abcdef12345678"
	want := common.HexToAddress(addrHex)
	if got := Address(t, "f", addrHex); got != want {
		t.Errorf("Address(%s) = %s, want %s", addrHex, got.Hex(), want.Hex())
	}
	if got := Address(t, "f", ""); got != (common.Address{}) {
		t.Errorf("Address(empty) = %s, want zero", got.Hex())
	}
}
