package reth

import (
	"encoding/json"
	"fmt"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/genesis"
	"github.com/nerolation/state-actor/internal/entitygen"
)

// writeChainSpec writes a Reth-compatible chainspec JSON to outPath.
//
// The alloc map is derived from accounts: each account's balance, nonce, code,
// and storage are written verbatim so that reth's state_root_ref_unhashed(&alloc)
// computes the same state root as our Go-side ComputeStateRoot — ensuring the
// genesis hash stored in MDBX + static files matches the chainspec.
//
// For large-scale generation (millions of accounts) this file may be large;
// callers may choose to pass nil accounts for scenarios where genesis-hash
// matching is not required (e.g. reth init-state import pipelines).
//
// `chainID` overrides `config.chainId` when non-zero. When genesisPath is
// non-empty, every top-level field from that file EXCEPT alloc is copied in.
func writeChainSpec(genesisPath, outPath string, chainID int64, accounts []*entitygen.Account) error {
	spec := buildChainSpec(chainID)

	if genesisPath != "" {
		raw, err := os.ReadFile(genesisPath)
		if err != nil {
			return fmt.Errorf("read genesis file: %w", err)
		}
		var src map[string]any
		if err := json.Unmarshal(raw, &src); err != nil {
			return fmt.Errorf("parse genesis JSON: %w", err)
		}
		for k, v := range src {
			if k == "alloc" {
				continue
			}
			spec[k] = v
		}
		if chainID != 0 {
			if cfg, ok := spec["config"].(map[string]any); ok {
				cfg["chainId"] = chainID
			}
		}
	}

	spec["alloc"] = buildAllocMap(accounts)

	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal chainspec: %w", err)
	}
	if err := os.WriteFile(outPath, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("write chainspec: %w", err)
	}
	return nil
}

// buildAllocMap converts a slice of entitygen accounts into the Go-Ethereum
// genesis alloc format that reth's JSON parser understands.
//
// Each entry is keyed by the 0x-prefixed checksummed address. Fields written:
//   - "balance": hex-encoded balance (0x + hex, no leading zeros except 0x0)
//   - "nonce": hex u64 (omitted if 0)
//   - "code": hex bytecode (omitted if empty or nil)
//   - "storage": map of 0x-prefixed slot→value (omitted if empty)
func buildAllocMap(accounts []*entitygen.Account) map[string]any {
	alloc := make(map[string]any, len(accounts))
	for _, acc := range accounts {
		if acc == nil || acc.StateAccount == nil {
			continue
		}
		entry := make(map[string]any)

		// Balance: hex string "0x..."
		bal := acc.StateAccount.Balance.ToBig()
		if bal == nil {
			bal = new(big.Int)
		}
		entry["balance"] = "0x" + fmt.Sprintf("%x", bal)

		// Nonce (omit if 0)
		if acc.StateAccount.Nonce != 0 {
			entry["nonce"] = fmt.Sprintf("0x%x", acc.StateAccount.Nonce)
		}

		// Code (omit if empty)
		if len(acc.Code) > 0 {
			entry["code"] = "0x" + common.Bytes2Hex(acc.Code)
		}

		// Storage (omit if empty)
		if len(acc.Storage) > 0 {
			storage := make(map[string]any, len(acc.Storage))
			for _, slot := range acc.Storage {
				k := "0x" + common.Bytes2Hex(slot.Key.Bytes())
				v := "0x" + common.Bytes2Hex(slot.Value.Bytes())
				storage[k] = v
			}
			entry["storage"] = storage
		}

		alloc[acc.Address.Hex()] = entry
	}
	return alloc
}

// buildChainSpec returns the default "dev-like" chainspec used when no
// --genesis file is provided. All post-Merge hardforks are activated at
// block 0 / timestamp 0 so the EVM supports current opcodes out of the box.
func buildChainSpec(chainID int64) map[string]any {
	if chainID == 0 {
		chainID = 1337
	}
	return map[string]any{
		"config": map[string]any{
			"chainId":                 chainID,
			"homesteadBlock":          0,
			"eip150Block":             0,
			"eip155Block":             0,
			"eip158Block":             0,
			"byzantiumBlock":          0,
			"constantinopleBlock":     0,
			"petersburgBlock":         0,
			"istanbulBlock":           0,
			"berlinBlock":             0,
			"londonBlock":             0,
			"mergeNetsplitBlock":      0,
			"shanghaiTime":            0,
			"cancunTime":              0,
			"terminalTotalDifficulty": 0,
		},
		"nonce":         "0x0",
		"timestamp":     "0x0",
		"extraData":     "0x",
		"gasLimit":      "0x1c9c380",
		"difficulty":    "0x0",
		"coinbase":      "0x0000000000000000000000000000000000000000",
		"mixHash":       "0x0000000000000000000000000000000000000000000000000000000000000000",
		"parentHash":    "0x0000000000000000000000000000000000000000000000000000000000000000",
		"baseFeePerGas": "0x3b9aca00",
		"blobGasUsed":   "0x0",
		"excessBlobGas": "0x0",
	}
}

// loadGenesisForReth wraps genesis.LoadGenesis. Kept as a thin indirection so
// the signature anchors Reth-specific expectations.
func loadGenesisForReth(path string) (*genesis.Genesis, error) {
	if path == "" {
		return nil, nil
	}
	g, err := genesis.LoadGenesis(path)
	if err != nil {
		return nil, fmt.Errorf("load genesis: %w", err)
	}
	return g, nil
}

// deriveChainID returns the chain ID that should be used for the Reth run.
// Priority: explicit override > genesis config > default 1337.
func deriveChainID(override int64, g *genesis.Genesis) int64 {
	if override > 0 {
		return override
	}
	if g != nil && g.Config != nil && g.Config.ChainID != nil {
		return g.Config.ChainID.Int64()
	}
	return 1337
}
