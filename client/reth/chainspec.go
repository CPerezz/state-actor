package reth

import (
	"encoding/json"
	"fmt"
	"os"

	"github.com/nerolation/state-actor/genesis"
)

// writeChainSpec writes a Reth-compatible chainspec JSON to outPath. The
// chainspec carries the chain config (chainId + hardfork activations) and the
// genesis block header fields. Its alloc is intentionally EMPTY: `reth
// init-state` reads the authoritative state (and state root) from the JSONL
// dump, so duplicating accounts here would be wasted bytes and a second
// source of truth that could drift.
//
// When genesisPath is non-empty, every top-level field from that file EXCEPT
// alloc is copied into the chainspec, so user-provided header fields
// (timestamp, gasLimit, baseFeePerGas, fork activations under "config") flow
// through verbatim. chainID overrides config.chainId when non-zero.
func writeChainSpec(genesisPath, outPath string, chainID int64) error {
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

	// Empty alloc: state comes from the JSONL dump via `reth init-state`.
	spec["alloc"] = map[string]any{}

	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal chainspec: %w", err)
	}
	if err := os.WriteFile(outPath, append(out, '\n'), 0o644); err != nil {
		return fmt.Errorf("write chainspec: %w", err)
	}
	return nil
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
// the signature anchors Reth-specific expectations (e.g. if we later need to
// reject certain genesis fields that Reth doesn't accept).
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
