// Package testhex provides hex-string parsing helpers for fixture-driven
// tests (oracle differential tests, integration tests). Every helper takes
// a *testing.T and t.Fatalf's on malformed input — silently zeroing a
// malformed hex field once produced wrong-genesis-hash failures that took
// hours to track back to a fixture typo (commit 1362de0; besu PR #29).
//
// Tolerates leading zeros (e.g. "0x0102030405060708" from Besu / Parity
// chainspec fixtures). go-ethereum's hexutil rejects those for fixed-width
// types and is wrong for fixture-loading; that's why these helpers exist.
package testhex

import (
	"encoding/hex"
	"math/big"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// stripPrefix removes a leading "0x" or "0X" if present. Returns the
// remaining string. An input of "" or "0x" alone returns "".
func stripPrefix(s string) string {
	return strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
}

// Uint64 parses a 0x-prefixed hex string into a uint64. Empty input (or
// bare "0x") returns 0. Non-hex digits, or values that overflow uint64,
// fail the test loud — the caller's fixture is wrong.
func Uint64(t *testing.T, field, s string) uint64 {
	t.Helper()
	stripped := stripPrefix(s)
	if stripped == "" {
		return 0
	}
	v := new(big.Int)
	if _, ok := v.SetString(stripped, 16); !ok {
		t.Fatalf("hex parse %s: not hex: %q", field, s)
	}
	if !v.IsUint64() {
		t.Fatalf("hex parse %s: value overflows uint64: %q", field, s)
	}
	return v.Uint64()
}

// Big parses a 0x-prefixed hex string into a *big.Int. Empty input (or
// bare "0x") returns a zero-valued *big.Int (not nil). Non-hex digits
// fail the test loud.
func Big(t *testing.T, field, s string) *big.Int {
	t.Helper()
	v := new(big.Int)
	stripped := stripPrefix(s)
	if stripped == "" {
		return v
	}
	if _, ok := v.SetString(stripped, 16); !ok {
		t.Fatalf("hex parse %s: not hex: %q", field, s)
	}
	return v
}

// Bytes parses a 0x-prefixed hex string into a byte slice. Empty input
// (or bare "0x") returns nil. Odd-length input is left-padded with one
// nibble of zero (Parity / Besu fixtures emit "0x0" for slot 0). Bad
// hex fails the test loud.
func Bytes(t *testing.T, field, s string) []byte {
	t.Helper()
	stripped := stripPrefix(s)
	if stripped == "" {
		return nil
	}
	if len(stripped)%2 == 1 {
		stripped = "0" + stripped
	}
	b, err := hex.DecodeString(stripped)
	if err != nil {
		t.Fatalf("hex parse %s: %v (input %q)", field, err, s)
	}
	return b
}

// Hash parses a 0x-prefixed hex string into a 32-byte common.Hash.
// Empty input (or bare "0x") returns the zero hash. Length mismatch
// fails the test loud — common.HexToHash silently truncates/zeros
// shorter or longer inputs, which masks fixture corruption.
func Hash(t *testing.T, field, s string) common.Hash {
	t.Helper()
	if s == "" || s == "0x" {
		return common.Hash{}
	}
	b := Bytes(t, field, s)
	if len(b) != common.HashLength {
		t.Fatalf("hex parse %s: expected %d bytes, got %d (%q)", field, common.HashLength, len(b), s)
	}
	var h common.Hash
	copy(h[:], b)
	return h
}

// Address parses a 0x-prefixed hex string into a 20-byte common.Address.
// Empty input (or bare "0x") returns the zero address. Length mismatch
// fails the test loud (same rationale as Hash).
func Address(t *testing.T, field, s string) common.Address {
	t.Helper()
	if s == "" || s == "0x" {
		return common.Address{}
	}
	b := Bytes(t, field, s)
	if len(b) != common.AddressLength {
		t.Fatalf("hex parse %s: expected %d bytes, got %d (%q)", field, common.AddressLength, len(b), s)
	}
	var a common.Address
	copy(a[:], b)
	return a
}
