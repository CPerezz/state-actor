//go:build cgo_neth

package nethermind

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/internal/neth"
)

// TestDifferentialOracle is the Tier 2 differential oracle: state-actor's
// genesis-block hash for each of the three vendored Parity chainspec
// fixtures (from Nethermind.Blockchain.Test.GenesisBuilderTests at upstream
// SHA 09bd5a2d) MUST byte-equal the golden hash Nethermind itself
// computes. This pins the encoding/HalfPath/StackTrie pipeline against an
// external reference and guards against silent drift.
//
// Citation: src/Nethermind/Nethermind.Blockchain.Test/GenesisBuilderTests.cs
// at SHA 09bd5a2d, lines 26-34. The test class loads each fixture via
// Nethermind's ChainSpec parser and asserts `Block.Hash` equals the
// hard-coded value below.
func TestDifferentialOracle(t *testing.T) {
	fixturesDir := filepath.Join("..", "..", "internal", "neth", "testdata")

	cases := []struct {
		name             string
		fixtureFile      string
		wantGenesisHash  string
	}{
		{
			name:            "empty_accounts_and_storages",
			fixtureFile:     "empty_accounts_and_storages.json",
			wantGenesisHash: "0x61b2253366eab37849d21ac066b96c9de133b8c58a9a38652deae1dd7ec22e7b",
		},
		{
			name:            "empty_accounts_and_codes",
			fixtureFile:     "empty_accounts_and_codes.json",
			wantGenesisHash: "0xfa3da895e1c2a4d2673f60dd885b867d60fb6d823abaf1e5276a899d7e2feca5",
		},
		{
			name:            "hive_zero_balance_test",
			fixtureFile:     "hive_zero_balance_test.json",
			wantGenesisHash: "0x62839401df8970ec70785f62e9e9d559b256a9a10b343baf6c064747b094de09",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			spec, err := loadParityChainspec(filepath.Join(fixturesDir, tc.fixtureFile))
			if err != nil {
				t.Fatalf("load %s: %v", tc.fixtureFile, err)
			}

			accounts, codes, storages := paritySpecToStateAccounts(spec)
			t.Logf("%s: %d allocations → %d state entries (after EIP-161 filter), %d with storage",
				tc.name, len(spec.Accounts), len(accounts), len(storages))

			tmpDir := t.TempDir()
			dbs, err := openNethDBs(tmpDir)
			if err != nil {
				t.Fatalf("open DBs: %v", err)
			}
			defer dbs.Close()

			stateRoot := common.Hash(neth.EmptyTreeHash)
			if len(accounts) > 0 {
				stateRoot, err = writeGenesisAllocAccounts(dbs, accounts, codes, storages)
				if err != nil {
					t.Fatalf("write genesis alloc: %v", err)
				}
			}

			header, err := buildHeaderFromParitySpec(spec, stateRoot)
			if err != nil {
				t.Fatalf("build header: %v", err)
			}

			got := header.Hash().Hex()
			if !strings.EqualFold(got, tc.wantGenesisHash) {
				t.Errorf("genesis hash mismatch\n  got:  %s\n  want: %s\n  state root: %s",
					got, tc.wantGenesisHash, stateRoot.Hex())
			}
		})
	}
}

// parityChainspec is a minimal Parity-format chainspec — only the fields
// that affect the genesis block hash. Hex fields are parsed manually
// because the Parity emitter writes leading zeros (e.g. "0x0a10000000"),
// which go-ethereum's hexutil rejects for the Uint64 type.
type parityChainspec struct {
	Genesis struct {
		Difficulty string `json:"difficulty"`
		Author     string `json:"author"`
		Timestamp  string `json:"timestamp"`
		ParentHash string `json:"parentHash"`
		ExtraData  string `json:"extraData"`
		GasLimit   string `json:"gasLimit"`
		Seal       struct {
			Ethereum struct {
				Nonce   string `json:"nonce"`
				MixHash string `json:"mixHash"`
			} `json:"ethereum"`
		} `json:"seal"`
	} `json:"genesis"`
	Params struct {
		ChainID string `json:"chainID"`
	} `json:"params"`
	Accounts map[string]parityAccount `json:"accounts"`
}

type parityAccount struct {
	Balance string            `json:"balance"`
	Nonce   string            `json:"nonce"`
	Code    string            `json:"code"`
	Storage map[string]string `json:"storage"`
	Builtin *json.RawMessage  `json:"builtin"`
}

// utf8BOM is the byte sequence some editors prepend to JSON files.
// Nethermind's hive_zero_balance_test.json carries one; encoding/json
// rejects it as `invalid character 'ï' looking for beginning of value`.
var utf8BOM = []byte{0xEF, 0xBB, 0xBF}

func loadParityChainspec(path string) (*parityChainspec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	data = bytes.TrimPrefix(data, utf8BOM)
	var spec parityChainspec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &spec, nil
}

// parseHexU64 parses a "0x..." string into a uint64, tolerating leading
// zero digits (which go-ethereum's hexutil.Uint64 rejects).
func parseHexU64(s string) (uint64, error) {
	if s == "" {
		return 0, nil
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return 0, nil
	}
	v := new(big.Int)
	if _, ok := v.SetString(s, 16); !ok {
		return 0, fmt.Errorf("not hex: %q", s)
	}
	if !v.IsUint64() {
		return 0, fmt.Errorf("hex value overflows uint64: %q", s)
	}
	return v.Uint64(), nil
}

// parseHexBig parses a "0x..." string into a big.Int, tolerating leading
// zeros and the empty string (returns 0).
func parseHexBig(s string) *big.Int {
	v := new(big.Int)
	if s == "" {
		return v
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return v
	}
	v.SetString(s, 16)
	return v
}

// parseHexBytes parses a "0x..."-prefixed hex string into bytes. Returns
// nil for the empty string or "0x".
func parseHexBytes(s string) []byte {
	if s == "" {
		return nil
	}
	s = strings.TrimPrefix(strings.TrimPrefix(s, "0x"), "0X")
	if s == "" {
		return nil
	}
	if len(s)%2 == 1 {
		s = "0" + s
	}
	b, err := hex.DecodeString(s)
	if err != nil {
		return nil
	}
	return b
}

// paritySpecToStateAccounts converts Parity allocations into the
// (StateAccount, code, storage) triple writeGenesisAllocAccounts expects.
// Empty accounts (EIP-161-empty: zero balance, zero nonce, no code, no
// storage) and `builtin`-only entries (precompile gas-pricing
// pseudoaccounts) are dropped — Nethermind's GenesisBuilder skips them at
// genesis time and the state root we compute has to match.
func paritySpecToStateAccounts(spec *parityChainspec) (
	map[common.Address]*types.StateAccount,
	map[common.Address][]byte,
	map[common.Address]map[common.Hash]common.Hash,
) {
	accounts := make(map[common.Address]*types.StateAccount)
	codes := make(map[common.Address][]byte)
	storages := make(map[common.Address]map[common.Hash]common.Hash)

	for addrStr, acc := range spec.Accounts {
		balance := parseHexBig(acc.Balance)
		nonce, _ := parseHexU64(acc.Nonce)
		code := parseHexBytes(acc.Code)

		// Nethermind's GenesisBuilder treats `balance` PRESENCE as the
		// keep/drop signal, not its value: `hive_zero_balance_test.json`
		// includes a `0x...03` precompile with `"balance": "0x0"` and the
		// expected golden hash has it persisted in state. So drop only
		// when balance is ABSENT (the hexutil zero-value here is "").
		hasExplicitBalance := acc.Balance != ""

		// Builtin pseudoaccounts without any state field at all (no
		// balance, nonce, code, or storage) are gas-pricing entries only.
		isBuiltinOnly := acc.Builtin != nil &&
			!hasExplicitBalance &&
			nonce == 0 &&
			len(code) == 0 &&
			len(acc.Storage) == 0
		if isBuiltinOnly {
			continue
		}

		// EIP-161 empty: balance=0 AND not explicit, nonce=0, code=0x,
		// storage={}. Explicit balance keeps the account regardless of value.
		isEmpty := !hasExplicitBalance &&
			balance.Sign() == 0 &&
			nonce == 0 &&
			len(code) == 0 &&
			len(acc.Storage) == 0
		if isEmpty {
			continue
		}

		addr := common.HexToAddress(addrStr)
		bal256, _ := uint256.FromBig(balance)
		sa := &types.StateAccount{
			Nonce:    nonce,
			Balance:  bal256,
			Root:     types.EmptyRootHash,
			CodeHash: types.EmptyCodeHash[:],
		}
		if len(code) > 0 {
			codes[addr] = code
		}
		if len(acc.Storage) > 0 {
			normalized := make(map[common.Hash]common.Hash, len(acc.Storage))
			for k, v := range acc.Storage {
				kh := common.HexToHash(k)
				vh := common.HexToHash(v)
				if vh == (common.Hash{}) {
					continue // zero slot = deletion, skip
				}
				normalized[kh] = vh
			}
			if len(normalized) > 0 {
				storages[addr] = normalized
			}
		}
		accounts[addr] = sa
	}

	return accounts, codes, storages
}

// buildHeaderFromParitySpec builds a go-ethereum types.Header whose RLP
// keccak yields the same genesis hash Nethermind computes — every field
// Nethermind hashes is sourced from the Parity chainspec, with the
// state root provided by the caller.
//
// Hash-relevant fields that Nethermind sets at genesis (per
// HeaderDecoder.cs at SHA 09bd5a2d) and how we mirror each:
//   ParentHash   ← spec.Genesis.ParentHash
//   UncleHash    ← types.EmptyUncleHash (no uncles at genesis, fixed)
//   Coinbase     ← spec.Genesis.Author
//   Root         ← caller's stateRoot (already computed)
//   TxHash       ← types.EmptyTxsHash (genesis has no txs, fixed)
//   ReceiptHash  ← types.EmptyReceiptsHash (genesis has no receipts, fixed)
//   Bloom        ← zero (no logs at genesis, fixed)
//   Difficulty   ← spec.Genesis.Difficulty
//   Number       ← 0 (genesis is block 0, fixed)
//   GasLimit     ← spec.Genesis.GasLimit
//   GasUsed      ← 0 (genesis has no execution, fixed)
//   Time         ← spec.Genesis.Timestamp
//   Extra        ← spec.Genesis.ExtraData
//   MixDigest    ← spec.Genesis.Seal.Ethereum.MixHash
//   Nonce        ← spec.Genesis.Seal.Ethereum.Nonce
//
// Optional EIP-1559 / Shanghai / Cancun / Prague fields stay nil — the
// three vendored fixtures all target Berlin or earlier (the test class
// uses `Berlin.Instance` as ISpecProvider), so no `baseFeePerGas` or
// `withdrawalsRoot` are emitted in the genesis header.
func buildHeaderFromParitySpec(spec *parityChainspec, stateRoot common.Hash) (*types.Header, error) {
	gasLimit, err := parseHexU64(spec.Genesis.GasLimit)
	if err != nil {
		return nil, fmt.Errorf("gasLimit: %w", err)
	}
	timestamp, err := parseHexU64(spec.Genesis.Timestamp)
	if err != nil {
		return nil, fmt.Errorf("timestamp: %w", err)
	}

	var nonce types.BlockNonce
	nb := parseHexBytes(spec.Genesis.Seal.Ethereum.Nonce)
	if len(nb) > 8 {
		return nil, fmt.Errorf("nonce too long: %d bytes", len(nb))
	}
	copy(nonce[8-len(nb):], nb)

	return &types.Header{
		ParentHash:  common.HexToHash(spec.Genesis.ParentHash),
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    common.HexToAddress(spec.Genesis.Author),
		Root:        stateRoot,
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Bloom:       types.Bloom{},
		Difficulty:  parseHexBig(spec.Genesis.Difficulty),
		Number:      new(big.Int),
		GasLimit:    gasLimit,
		GasUsed:     0,
		Time:        timestamp,
		Extra:       parseHexBytes(spec.Genesis.ExtraData),
		MixDigest:   common.HexToHash(spec.Genesis.Seal.Ethereum.MixHash),
		Nonce:       nonce,
	}, nil
}
