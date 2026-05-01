package reth

import (
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/common/hexutil"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/params"
	"github.com/nerolation/state-actor/genesis"
)

// buildBlock0Header builds the canonical genesis block header for the cgo
// direct-write path. Number=0; ParentHash=zero; Root must be the actual state
// root computed from the generated entities (or types.EmptyRootHash for
// empty alloc). The header.Hash() must match the chainspec-derived genesis
// hash that reth boot validates.
func buildBlock0Header(g *genesis.Genesis, chainID int64) (*types.Header, error) {
	return buildHeaderFromGenesis(g, chainID, 0, common.Hash{}, types.EmptyRootHash)
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
