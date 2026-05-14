package rpcprobe

import (
	"encoding/hex"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
)

// 4-byte function selectors for ERC-20's view methods. These are the
// first 4 bytes of keccak256(signature). Hardcoded so this file has no
// runtime dependency on the go-ethereum/accounts/abi package — these
// selectors are part of the ERC-20 spec and won't change.
const (
	selectorName        = "06fdde03" // name()
	selectorSymbol      = "95d89b41" // symbol()
	selectorDecimals    = "313ce567" // decimals()
	selectorTotalSupply = "18160ddd" // totalSupply()
	selectorBalanceOf   = "70a08231" // balanceOf(address)
	selectorAllowance   = "dd62ed3e" // allowance(address,address)
)

// ethCallStringResult sends an eth_call with calldata=`selector`, expects
// the response to be ABI-encoded `string` (dynamic type: 32-byte offset,
// 32-byte length, bytes right-padded to a 32-byte multiple), and returns
// the decoded UTF-8 string. Used for ERC-20 name() and symbol().
func ethCallStringResult(url string, to common.Address, selector string, block string) (string, error) {
	raw, err := ethCallHex(url, to, selector, block)
	if err != nil {
		return "", err
	}
	if len(raw) < 64 {
		return "", fmt.Errorf("string response too short (len=%d, want >=64): %x", len(raw), raw)
	}
	// First 32 bytes: offset (expected 0x20).
	// Next 32 bytes: length.
	lengthBytes := raw[32:64]
	length := uint256.NewInt(0).SetBytes(lengthBytes).Uint64()
	if length == 0 {
		return "", nil
	}
	if uint64(len(raw)) < 64+length {
		return "", fmt.Errorf("string response truncated: declared length=%d, have %d data bytes", length, len(raw)-64)
	}
	return string(raw[64 : 64+length]), nil
}

// ethCallUint256Result sends an eth_call with the given calldata and
// decodes the 32-byte response as a uint256. Used for ERC-20 totalSupply,
// balanceOf, and allowance.
func ethCallUint256Result(url string, to common.Address, calldata string, block string) (*uint256.Int, error) {
	raw, err := ethCallHex(url, to, calldata, block)
	if err != nil {
		return nil, err
	}
	if len(raw) != 32 {
		return nil, fmt.Errorf("uint256 response length=%d, want 32: %x", len(raw), raw)
	}
	u := new(uint256.Int).SetBytes(raw)
	return u, nil
}

// ethCallUint8Result sends an eth_call and decodes the 32-byte response
// as a uint8 (last byte). Used for ERC-20 decimals().
func ethCallUint8Result(url string, to common.Address, calldata string, block string) (uint8, error) {
	raw, err := ethCallHex(url, to, calldata, block)
	if err != nil {
		return 0, err
	}
	if len(raw) != 32 {
		return 0, fmt.Errorf("uint8 response length=%d, want 32: %x", len(raw), raw)
	}
	// Solidity ABI-encodes uint8 as 32 bytes (right-aligned).
	for i := 0; i < 31; i++ {
		if raw[i] != 0 {
			return 0, fmt.Errorf("uint8 response has high bytes set (likely a wider type): %x", raw)
		}
	}
	return raw[31], nil
}

// ethCallHex is the low-level helper: sends eth_call(to=addr, data=hex)
// at the given block tag and returns the decoded response bytes (after
// stripping the 0x prefix). Caller handles ABI decoding.
func ethCallHex(url string, to common.Address, calldataHex string, block string) ([]byte, error) {
	calldataHex = strings.TrimPrefix(calldataHex, "0x")
	payload := map[string]any{
		"to":   to.Hex(),
		"data": "0x" + calldataHex,
	}
	rawResp, err := Call(url, "eth_call", []any{payload, block})
	if err != nil {
		return nil, err
	}
	var hexStr string
	if err := json.Unmarshal(rawResp, &hexStr); err != nil {
		return nil, fmt.Errorf("unmarshal eth_call result: %w (raw: %s)", err, rawResp)
	}
	hexStr = strings.TrimPrefix(hexStr, "0x")
	if hexStr == "" {
		return []byte{}, nil
	}
	if len(hexStr)%2 == 1 {
		hexStr = "0" + hexStr
	}
	b, err := hex.DecodeString(hexStr)
	if err != nil {
		return nil, fmt.Errorf("decode eth_call result hex: %w", err)
	}
	return b, nil
}

// padAddressArg returns 32-byte left-padded hex (no 0x prefix) for an
// ABI address arg. Address occupies the rightmost 20 bytes; the leading
// 12 bytes are zero.
func padAddressArg(a common.Address) string {
	var buf [32]byte
	copy(buf[12:], a[:])
	return hex.EncodeToString(buf[:])
}

// EthCallERC20Name calls `name()` on an ERC-20 contract.
func EthCallERC20Name(url string, token common.Address, block string) (string, error) {
	return ethCallStringResult(url, token, selectorName, block)
}

// EthCallERC20Symbol calls `symbol()` on an ERC-20 contract.
func EthCallERC20Symbol(url string, token common.Address, block string) (string, error) {
	return ethCallStringResult(url, token, selectorSymbol, block)
}

// EthCallERC20Decimals calls `decimals()`.
func EthCallERC20Decimals(url string, token common.Address, block string) (uint8, error) {
	return ethCallUint8Result(url, token, selectorDecimals, block)
}

// EthCallERC20TotalSupply calls `totalSupply()`.
func EthCallERC20TotalSupply(url string, token common.Address, block string) (*uint256.Int, error) {
	return ethCallUint256Result(url, token, selectorTotalSupply, block)
}

// EthCallERC20BalanceOf calls `balanceOf(holder)`.
func EthCallERC20BalanceOf(url string, token, holder common.Address, block string) (*uint256.Int, error) {
	return ethCallUint256Result(url, token, selectorBalanceOf+padAddressArg(holder), block)
}

// EthCallERC20Allowance calls `allowance(owner, spender)`.
func EthCallERC20Allowance(url string, token, owner, spender common.Address, block string) (*uint256.Int, error) {
	return ethCallUint256Result(url, token, selectorAllowance+padAddressArg(owner)+padAddressArg(spender), block)
}
