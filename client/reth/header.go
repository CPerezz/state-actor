package reth

import (
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/ethereum/go-ethereum/rlp"
	"github.com/nerolation/state-actor/genesis"
)

// buildBlock0Header builds the chainspec-derived genesis block header
// (Number=0, Root=emptyTrieHash). Used to compute the parent_hash that
// block 1 must carry — Reth's init-state writes block 0 from the chainspec
// first, then appends our block 1, so the two must link up.
func buildBlock0Header(g *genesis.Genesis, chainID int64) (*types.Header, error) {
	return buildHeaderFromGenesis(g, chainID, 0, common.Hash{}, types.EmptyRootHash)
}

// buildBlock1Header builds block 1 carrying our generated state. Block 1
// is what `reth init-state --without-evm --header` appends on top of the
// chainspec-derived block 0.
//
// Why block 1 and not block 0: Reth v2.1.0's `setup_without_evm` has a
// u64 underflow (`header.number() - 1`) when number=0, which sends
// `append_dummy_chain` into an infinite loop that eventually OOMs. Using
// number=1 makes the range `1..=0` empty — no dummy chain is built, and
// our block is appended directly. The resulting state is queryable at
// block 1, which the dev node immediately mines on top of.
//
// parent_hash is computed from the chainspec's block 0 header so the
// chain links up.
func buildBlock1Header(g *genesis.Genesis, chainID int64, stateRoot, parentHash common.Hash) (*types.Header, error) {
	return buildHeaderFromGenesis(g, chainID, 1, parentHash, stateRoot)
}

// buildHeaderFromGenesis constructs a header from a chainspec/Genesis at
// an arbitrary block number, with a given stateRoot + parentHash.
//
// Fields mirror the WriteGenesisBlock path in genesis/genesis.go:165-232
// (EIP-1559 base fee, Shanghai withdrawals hash, Cancun blob fields,
// Prague requests hash). The key invariant is that running this on
// number=0 with emptyRoot produces the same hash Reth's chainspec parser
// computes for block 0 — otherwise block 1's parent_hash won't link up.
func buildHeaderFromGenesis(g *genesis.Genesis, chainID int64, number uint64, parentHash, stateRoot common.Hash) (*types.Header, error) {
	if g == nil {
		g = defaultGenesisForReth(chainID)
	} else if chainID != 0 {
		cfgCopy := *g.Config
		cfgCopy.ChainID = big.NewInt(chainID)
		g.Config = &cfgCopy
	}

	header := &types.Header{
		Number:      new(big.Int).SetUint64(number),
		Nonce:       types.EncodeNonce(uint64(g.Nonce)),
		Time:        uint64(g.Timestamp),
		ParentHash:  parentHash,
		Extra:       g.ExtraData,
		GasLimit:    uint64(g.GasLimit),
		GasUsed:     uint64(g.GasUsed),
		Difficulty:  (*big.Int)(g.Difficulty),
		MixDigest:   g.Mixhash,
		Coinbase:    g.Coinbase,
		Root:        stateRoot,
		// Canonical empty-body hashes. go-ethereum leaves these as zero when
		// constructing a header struct directly, but reth encodes them as their
		// canonical values (EmptyUncleHash = keccak256(rlp([])), EmptyTxsHash =
		// EmptyRootHash = keccak256(rlp([]))). Without these, go-ethereum's
		// header.Hash() produces a different value than reth's genesis hash.
		UncleHash:   types.EmptyUncleHash,
		TxHash:      types.EmptyTxsHash,
		ReceiptHash: types.EmptyRootHash,
	}
	if header.GasLimit == 0 {
		header.GasLimit = params.GenesisGasLimit
	}
	if header.Difficulty == nil {
		header.Difficulty = big.NewInt(0)
	}
	if header.Extra == nil {
		header.Extra = []byte{}
	}

	num := new(big.Int).SetUint64(number)
	ts := uint64(g.Timestamp)

	if g.Config.IsLondon(num) {
		if g.BaseFee != nil {
			header.BaseFee = (*big.Int)(g.BaseFee)
		} else {
			header.BaseFee = new(big.Int).SetUint64(params.InitialBaseFee)
		}
	}
	if g.Config.IsShanghai(num, ts) {
		emptyWithdrawalsHash := types.EmptyWithdrawalsHash
		header.WithdrawalsHash = &emptyWithdrawalsHash
	}
	if g.Config.IsCancun(num, ts) {
		header.ParentBeaconRoot = new(common.Hash)
		if g.ExcessBlobGas != nil {
			excess := uint64(*g.ExcessBlobGas)
			header.ExcessBlobGas = &excess
		} else {
			header.ExcessBlobGas = new(uint64)
		}
		if g.BlobGasUsed != nil {
			used := uint64(*g.BlobGasUsed)
			header.BlobGasUsed = &used
		} else {
			header.BlobGasUsed = new(uint64)
		}
	}
	if g.Config.IsPrague(num, ts) {
		emptyRequestsHash := types.EmptyRequestsHash
		header.RequestsHash = &emptyRequestsHash
	}
	return header, nil
}

// writeHeaderFile writes the RLP-encoded header to outPath and returns
// keccak256(rlp) — the hash Reth expects via --header-hash.
func writeHeaderFile(header *types.Header, outPath string) (common.Hash, error) {
	encoded, err := rlp.EncodeToBytes(header)
	if err != nil {
		return common.Hash{}, fmt.Errorf("rlp encode header: %w", err)
	}
	if err := os.WriteFile(outPath, encoded, 0o644); err != nil {
		return common.Hash{}, fmt.Errorf("write header file: %w", err)
	}
	return header.Hash(), nil
}

// defaultGenesisForReth mirrors buildChainSpec(chainID). Used when the
// caller did not pass a --genesis file; produces a header consistent
// with the tiny "dev-like" chainspec.
func defaultGenesisForReth(chainID int64) *genesis.Genesis {
	if chainID == 0 {
		chainID = 1337
	}
	bf := big.NewInt(int64(params.InitialBaseFee))
	return &genesis.Genesis{
		Config: &params.ChainConfig{
			ChainID:                 big.NewInt(chainID),
			HomesteadBlock:          common.Big0,
			EIP150Block:             common.Big0,
			EIP155Block:             common.Big0,
			EIP158Block:             common.Big0,
			ByzantiumBlock:          common.Big0,
			ConstantinopleBlock:     common.Big0,
			PetersburgBlock:         common.Big0,
			IstanbulBlock:           common.Big0,
			BerlinBlock:             common.Big0,
			LondonBlock:             common.Big0,
			MergeNetsplitBlock:      common.Big0,
			ShanghaiTime:            ptrU64(0),
			CancunTime:              ptrU64(0),
			TerminalTotalDifficulty: common.Big0,
		},
		Nonce:      0,
		Timestamp:  0,
		ExtraData:  []byte{},
		GasLimit:   30_000_000,
		Difficulty: nil,
		Mixhash:    common.Hash{},
		Coinbase:   common.Address{},
		ParentHash: common.Hash{},
		BaseFee:    (*hexutil.Big)(bf),
	}
}

func ptrU64(v uint64) *uint64 { return &v }
