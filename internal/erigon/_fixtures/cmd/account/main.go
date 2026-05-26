//go:build erigon_gen

// Command erigon-fixture-account regenerates byte-equality golden
// fixtures for the pure-Go internal/erigon/account writer by running
// Erigon's reference Account.EncodeForStorage on a fixed corpus of
// account shapes and writing the result to a JSON file.
//
// Run:
//
//	cd internal/erigon/_fixtures
//	go run -tags erigon_gen ./cmd/account \
//	    --out=../../account/testdata/erigon_golden.json
package main

import (
	"encoding/hex"
	"encoding/json"
	"flag"
	"fmt"
	"math/big"
	"os"

	"github.com/erigontech/erigon/common"
	"github.com/erigontech/erigon/execution/types/accounts"
	"github.com/holiman/uint256"
)

type fixture struct {
	Label       string `json:"label"`
	Nonce       uint64 `json:"nonce"`
	BalanceHex  string `json:"balance_hex"`
	Incarnation uint64 `json:"incarnation"`
	CodeHashHex string `json:"code_hash_hex"`
	ExpectedHex string `json:"expected_hex"`
}

func main() {
	out := flag.String("out", "", "output JSON file path (required)")
	flag.Parse()
	if *out == "" {
		fmt.Fprintln(os.Stderr, "usage: account --out=<path>")
		os.Exit(2)
	}

	// Curated input shapes. Each exercises a different combination of
	// fieldSet bits + encoding lengths.
	var contractCode = []byte{0x60, 0x80, 0x60, 0x40, 0x52} // sample evm
	contractCodeHashCommon := common.BytesToHash(keccak(contractCode))
	contractCodeHash := accounts.InternCodeHash(contractCodeHashCommon)
	emptyCH := accounts.EmptyCodeHash

	corpora := []struct {
		label       string
		nonce       uint64
		balance     *big.Int
		incarnation uint64
		codeHash    accounts.CodeHash
		// codeHashCommonHex is what we record in the fixture JSON for
		// the test side (state-actor's account package keys on the
		// raw [32]byte form, not the interned handle).
		codeHashCommonHex string
	}{
		// fieldSet = 0 → empty account, 1 byte output.
		{"empty", 0, big.NewInt(0), 0, emptyCH, emptyCHHex()},
		// fieldSet = 1 → nonce-only.
		{"nonce1", 1, big.NewInt(0), 0, emptyCH, emptyCHHex()},
		{"nonce_max_uint64", ^uint64(0), big.NewInt(0), 0, emptyCH, emptyCHHex()},
		// fieldSet = 2 → balance-only.
		{"balance_1wei", 0, big.NewInt(1), 0, emptyCH, emptyCHHex()},
		{"balance_1eth", 0, mustBigInt("1000000000000000000"), 0, emptyCH, emptyCHHex()},
		{"balance_2_256m1", 0, twoTo256Minus1(), 0, emptyCH, emptyCHHex()},
		// fieldSet = 8 → codeHash-only (cleared account holding a contract code).
		{"code_only", 0, big.NewInt(0), 0, contractCodeHash, hex.EncodeToString(contractCodeHashCommon[:])},
		// fieldSet = 4 → incarnation-only (uncommon but legal).
		{"incarnation_only", 0, big.NewInt(0), 7, emptyCH, emptyCHHex()},
		// fieldSet = 15 → all four fields set.
		{"full_contract", 2, mustBigInt("1000"), 4, contractCodeHash, hex.EncodeToString(contractCodeHashCommon[:])},
		// Realistic mainnet-shaped EOA: nonce + balance, no code.
		{"eoa_realistic", 42, mustBigInt("123456789000000000"), 0, emptyCH, emptyCHHex()},
	}

	fixtures := make([]fixture, 0, len(corpora))
	for _, c := range corpora {
		a := accounts.Account{
			Nonce:       c.nonce,
			Balance:     *uint256.MustFromBig(c.balance),
			Incarnation: c.incarnation,
			CodeHash:    c.codeHash,
		}
		buf := make([]byte, a.EncodingLengthForStorage())
		a.EncodeForStorage(buf)
		fixtures = append(fixtures, fixture{
			Label:       c.label,
			Nonce:       c.nonce,
			BalanceHex:  hex.EncodeToString(c.balance.Bytes()),
			Incarnation: c.incarnation,
			CodeHashHex: c.codeHashCommonHex,
			ExpectedHex: hex.EncodeToString(buf),
		})
	}

	data, err := json.MarshalIndent(fixtures, "", "  ")
	if err != nil {
		fmt.Fprintln(os.Stderr, "marshal:", err)
		os.Exit(1)
	}
	if err := os.WriteFile(*out, append(data, '\n'), 0o644); err != nil {
		fmt.Fprintln(os.Stderr, "write:", err)
		os.Exit(1)
	}
	fmt.Fprintln(os.Stderr, "wrote", *out)
}

// emptyCHHex returns the hex of keccak256("") — the EmptyCodeHash bytes.
// Erigon exposes EmptyCodeHash as the interned CodeHash; we expand to the
// underlying [32]byte hex for the JSON record so state-actor's test
// keys on the same value.
func emptyCHHex() string {
	v := accounts.EmptyCodeHash.Value()
	return hex.EncodeToString(v[:])
}

func mustBigInt(s string) *big.Int {
	b, ok := new(big.Int).SetString(s, 10)
	if !ok {
		panic("bad big int: " + s)
	}
	return b
}

func twoTo256Minus1() *big.Int {
	b := new(big.Int).Lsh(big.NewInt(1), 256)
	return b.Sub(b, big.NewInt(1))
}

// keccak runs keccak256(data) without pulling go-ethereum into _fixtures.
// We reuse Erigon's hashing via the standard hash interface.
func keccak(data []byte) []byte {
	// Erigon ships keccak via golang.org/x/crypto/sha3.NewLegacyKeccak256
	// indirectly; the cheapest path here is to import the same.
	h := sha3NewLegacyKeccak256()
	h.Write(data)
	return h.Sum(nil)
}
