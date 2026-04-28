//go:build cgo_neth

package nethermind

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
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

			accounts, codes := paritySpecToStateAccounts(spec)
			t.Logf("%s: %d allocations → %d state entries (after EIP-161 filter)",
				tc.name, len(spec.Accounts), len(accounts))

			tmpDir := t.TempDir()
			dbs, err := openNethDBs(tmpDir)
			if err != nil {
				t.Fatalf("open DBs: %v", err)
			}
			defer dbs.Close()

			stateRoot := common.Hash(neth.EmptyTreeHash)
			if len(accounts) > 0 {
				stateRoot, err = writeGenesisAllocAccounts(dbs, accounts, codes)
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
// that affect the genesis block hash. Engine details and post-genesis
// transition tables are deliberately omitted; they don't influence the
// genesis we compute.
type parityChainspec struct {
	Genesis struct {
		Difficulty hexutil.Big    `json:"difficulty"`
		Author     common.Address `json:"author"`
		Timestamp  hexutil.Uint64 `json:"timestamp"`
		ParentHash common.Hash    `json:"parentHash"`
		ExtraData  hexutil.Bytes  `json:"extraData"`
		GasLimit   hexutil.Uint64 `json:"gasLimit"`
		Seal       struct {
			Ethereum struct {
				Nonce   hexutil.Bytes `json:"nonce"`
				MixHash common.Hash   `json:"mixHash"`
			} `json:"ethereum"`
		} `json:"seal"`
	} `json:"genesis"`
	Params struct {
		ChainID hexutil.Big `json:"chainID"`
	} `json:"params"`
	Accounts map[string]parityAccount `json:"accounts"`
}

type parityAccount struct {
	Balance *hexutil.Big      `json:"balance"`
	Nonce   *hexutil.Uint64   `json:"nonce"`
	Code    hexutil.Bytes     `json:"code"`
	Storage map[string]string `json:"storage"`
	Builtin *json.RawMessage  `json:"builtin"`
}

func loadParityChainspec(path string) (*parityChainspec, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	var spec parityChainspec
	if err := json.Unmarshal(data, &spec); err != nil {
		return nil, fmt.Errorf("unmarshal %s: %w", path, err)
	}
	return &spec, nil
}

// paritySpecToStateAccounts converts Parity allocations into the
// (StateAccount, code) pair writeGenesisAllocAccounts expects. Empty
// accounts (EIP-161-empty: zero balance, zero nonce, no code, no storage)
// and `builtin`-only entries (precompile gas-pricing pseudoaccounts) are
// dropped — Nethermind's GenesisBuilder skips them at genesis time and
// the state root we compute has to match.
func paritySpecToStateAccounts(spec *parityChainspec) (
	map[common.Address]*types.StateAccount,
	map[common.Address][]byte,
) {
	accounts := make(map[common.Address]*types.StateAccount)
	codes := make(map[common.Address][]byte)

	for addrStr, acc := range spec.Accounts {
		// Builtin-only entries (precompiles) carry no state.
		isBuiltinOnly := acc.Builtin != nil &&
			(acc.Balance == nil || (*big.Int)(acc.Balance).Sign() == 0) &&
			(acc.Nonce == nil || *acc.Nonce == 0) &&
			len(acc.Code) == 0 &&
			len(acc.Storage) == 0
		if isBuiltinOnly {
			continue
		}

		// EIP-161 empty: balance=0, nonce=0, code=0x, storage={}.
		balance := new(big.Int)
		if acc.Balance != nil {
			balance = (*big.Int)(acc.Balance)
		}
		nonce := uint64(0)
		if acc.Nonce != nil {
			nonce = uint64(*acc.Nonce)
		}
		isEmpty := balance.Sign() == 0 &&
			nonce == 0 &&
			len(acc.Code) == 0 &&
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
		if len(acc.Code) > 0 {
			codes[addr] = []byte(acc.Code)
		}
		// NOTE: storage entries on the parity account aren't written into
		// the per-account storage trie here — writeGenesisAllocAccounts'
		// code path is account-only. The three CCD-cited fixtures include
		// only a single account with one storage slot
		// (empty_accounts_and_storages.json's 0x6295...), so this
		// limitation is what blocks the full match for that fixture; the
		// other two pass against the empty-storage baseline.
		// TODO(B6 follow-up): wire genesis-alloc storage into
		// writeGenesisAllocAccounts.
		accounts[addr] = sa
	}

	return accounts, codes
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
	var nonce types.BlockNonce
	if len(spec.Genesis.Seal.Ethereum.Nonce) > 0 {
		nb := []byte(spec.Genesis.Seal.Ethereum.Nonce)
		if len(nb) > 8 {
			return nil, fmt.Errorf("nonce too long: %d bytes", len(nb))
		}
		copy(nonce[8-len(nb):], nb)
	}

	difficulty := new(big.Int)
	if d := (*big.Int)(&spec.Genesis.Difficulty); d != nil {
		difficulty = new(big.Int).Set(d)
	}

	return &types.Header{
		ParentHash:  spec.Genesis.ParentHash,
		UncleHash:   types.EmptyUncleHash,
		Coinbase:    spec.Genesis.Author,
		Root:        stateRoot,
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyReceiptsHash,
		Bloom:       types.Bloom{},
		Difficulty:  difficulty,
		Number:      new(big.Int),
		GasLimit:    uint64(spec.Genesis.GasLimit),
		GasUsed:     0,
		Time:        uint64(spec.Genesis.Timestamp),
		Extra:       []byte(spec.Genesis.ExtraData),
		MixDigest:   spec.Genesis.Seal.Ethereum.MixHash,
		Nonce:       nonce,
	}, nil
}
