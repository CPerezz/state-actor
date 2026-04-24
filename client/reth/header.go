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

// buildGenesisHeader constructs the genesis block header with the given
// stateRoot. All other fields come from the chainspec/genesis file (or
// defaults via buildChainSpec when no genesis file is provided).
//
// This header, RLP-encoded and paired with its keccak256 hash, is passed to
// `reth init-state --without-evm --header <path> --header-hash <hex>` so
// Reth uses our pre-computed state root for the genesis block instead of
// recomputing it from a chainspec alloc we'd rather not materialize.
//
// The header must match, field-for-field, what Reth would compute from the
// chainspec if we HAD included alloc — otherwise the genesis hash Reth
// stores in the DB won't agree with the hash we feed it, and subsequent
// `reth node` boots will reject the DB. Fields are mirrored from the
// WriteGenesisBlock path in genesis/genesis.go:165-232, which is known to
// produce a header geth accepts.
func buildGenesisHeader(g *genesis.Genesis, chainID int64, stateRoot common.Hash) (*types.Header, error) {
	// Synthesize a Genesis when the caller has none (the "no --genesis"
	// flow). Values mirror buildChainSpec's defaults so the two paths produce
	// byte-equivalent headers when alloc is the only differentiator.
	if g == nil {
		g = defaultGenesisForReth(chainID)
	} else if chainID != 0 {
		cfgCopy := *g.Config
		cfgCopy.ChainID = big.NewInt(chainID)
		g.Config = &cfgCopy
	}

	header := &types.Header{
		Number:     new(big.Int).SetUint64(uint64(g.Number)),
		Nonce:      types.EncodeNonce(uint64(g.Nonce)),
		Time:       uint64(g.Timestamp),
		ParentHash: g.ParentHash,
		Extra:      g.ExtraData,
		GasLimit:   uint64(g.GasLimit),
		GasUsed:    uint64(g.GasUsed),
		Difficulty: (*big.Int)(g.Difficulty),
		MixDigest:  g.Mixhash,
		Coinbase:   g.Coinbase,
		Root:       stateRoot,
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

	num := big.NewInt(int64(g.Number))
	ts := uint64(g.Timestamp)

	if g.Config.IsLondon(common.Big0) {
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
// keccak256(rlp) — the genesis block hash that Reth will use.
func writeHeaderFile(header *types.Header, outPath string) (common.Hash, error) {
	encoded, err := rlp.EncodeToBytes(header)
	if err != nil {
		return common.Hash{}, fmt.Errorf("rlp encode header: %w", err)
	}
	if err := os.WriteFile(outPath, encoded, 0o644); err != nil {
		return common.Hash{}, fmt.Errorf("write header file: %w", err)
	}
	// types.Header.Hash() is keccak(rlp(header)); we compute directly to
	// avoid an extra encode.
	return header.Hash(), nil
}

// defaultGenesisForReth mirrors buildChainSpec(chainID). Used when the
// caller did not pass a --genesis file; the resulting header is consistent
// with the tiny "dev-like" chainspec we emit in that case.
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

// ptrU64 is a helper for setting optional *uint64 fork activation times.
func ptrU64(v uint64) *uint64 { return &v }
