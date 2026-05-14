package rpcprobe

import (
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/crypto"
)

// TestSelectorsMatchKeccak pins each hardcoded selector to the first 4
// bytes of keccak256(signature). If the constants drift from the ERC-20
// signatures, this fails loudly instead of producing malformed eth_call
// payloads at runtime.
func TestSelectorsMatchKeccak(t *testing.T) {
	cases := []struct {
		signature string
		want      string
	}{
		{"name()", selectorName},
		{"symbol()", selectorSymbol},
		{"decimals()", selectorDecimals},
		{"totalSupply()", selectorTotalSupply},
		{"balanceOf(address)", selectorBalanceOf},
		{"allowance(address,address)", selectorAllowance},
	}
	for _, tc := range cases {
		t.Run(tc.signature, func(t *testing.T) {
			got := hex.EncodeToString(crypto.Keccak256([]byte(tc.signature))[:4])
			if got != tc.want {
				t.Errorf("selector for %s:\n got  %s\n want %s", tc.signature, got, tc.want)
			}
		})
	}
}

// TestPadAddressArgZero verifies an all-zero address pads to 32 zero bytes.
func TestPadAddressArgZero(t *testing.T) {
	got := padAddressArg(common.Address{})
	want := "0000000000000000000000000000000000000000000000000000000000000000"
	if got != want {
		t.Errorf("padAddressArg(0):\n got  %s\n want %s", got, want)
	}
}

// TestPadAddressArgNonZero verifies the address bytes land in the rightmost
// 20 bytes with 12 leading zero bytes.
func TestPadAddressArgNonZero(t *testing.T) {
	addr := common.HexToAddress("0x1234567890abcdef1234567890abcdef12345678")
	got := padAddressArg(addr)
	want := "0000000000000000000000001234567890abcdef1234567890abcdef12345678"
	if got != want {
		t.Errorf("padAddressArg(%s):\n got  %s\n want %s", addr.Hex(), got, want)
	}
}
