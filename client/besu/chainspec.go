package besu

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/nerolation/state-actor/genesis"
)

// ChainSpecFileName is the on-disk filename for the besu chainspec written
// by writeChainSpec. Lives directly under cfg.DBPath so smoke scripts can
// pass --genesis-file=<dbPath>/besu-chainspec.json without juggling extra
// mounts.
const ChainSpecFileName = "besu-chainspec.json"

// writeChainSpec renders a Besu-bootable chainspec JSON to <dbPath>/<file>
// from the in-memory cfg.Genesis.
//
// Fork-activation fields (`shanghaiTime`/`cancunTime`/`pragueTime`/
// `osakaTime`/`terminalTotalDifficulty`/`blobSchedule`) are emitted
// conditionally based on g.Config — same activation set the writer
// passes to internal/genesisheader.Build. Both sides MUST stay in
// sync: besu's cached-genesis path
// (BesuControllerBuilder.getGenesisState → GenesisState.fromStorage)
// rebuilds the genesis header from chainspec + cached stateRoot,
// then compares its hash against VARIABLES["chainHeadHash"]. Any
// field divergence produces "Supplied genesis block does not match
// chain data stored" at boot.
//
// alloc is always {} — state-actor writes state directly to RocksDB.
// `--genesis-state-hash-cache-enabled` at boot tells besu to trust
// the stored stateRoot rather than recompute from alloc.
//
// `ethash.fixeddifficulty` is emitted ONLY for pre-Merge chains (no
// terminalTotalDifficulty). For post-Merge chains it's dropped, since
// post-Merge consensus is engine-API-driven (or `--Xdev-mode-genesis-
// state-hash-cache-enabled` style besu dev mode — see C-followup).
func writeChainSpec(dbPath string, g *genesis.Genesis) (string, error) {
	if g == nil {
		return "", fmt.Errorf("besu writeChainSpec: nil genesis")
	}
	chainID := int64(1337)
	if g.Config != nil && g.Config.ChainID != nil {
		chainID = g.Config.ChainID.Int64()
	}
	gasLimit := uint64(g.GasLimit)
	if gasLimit == 0 {
		gasLimit = 30_000_000
	}
	extraDataHex := "0x"
	if len(g.ExtraData) > 0 {
		extraDataHex = "0x" + bytesToHex(g.ExtraData)
	}
	baseFeeHex := ""
	if g.BaseFee != nil {
		baseFeeHex = (*g.BaseFee).String()
	}
	// Difficulty must match the writer (genesisheader.Build pulls
	// g.Difficulty directly). BuildSynthetic emits 0 for post-Merge.
	diffHex := "0x0"
	if g.Difficulty != nil {
		diffHex = fmt.Sprintf("0x%x", g.Difficulty.ToInt())
	}

	cfg := map[string]any{
		"chainId":           chainID,
		"londonBlock":       0,
		"contractSizeLimit": 2147483647,
	}

	postMerge := g.Config != nil && g.Config.TerminalTotalDifficulty != nil
	if !postMerge {
		// Pre-Merge: ethash.fixeddifficulty enables dev-mode mining
		// with `--miner-enabled` + no external CL.
		cfg["ethash"] = map[string]any{
			"fixeddifficulty": 100,
		}
	} else {
		// Post-Merge: tell besu the chain is post-Merge by emitting
		// terminalTotalDifficulty. Block production switches to
		// engine-API / dev mode (configured at boot, not chainspec).
		cfg["terminalTotalDifficulty"] = "0x0"
	}

	if g.Config != nil {
		if g.Config.ShanghaiTime != nil {
			cfg["shanghaiTime"] = *g.Config.ShanghaiTime
		}
		if g.Config.CancunTime != nil {
			cfg["cancunTime"] = *g.Config.CancunTime
		}
		if g.Config.PragueTime != nil {
			cfg["pragueTime"] = *g.Config.PragueTime
		}
		if g.Config.OsakaTime != nil {
			cfg["osakaTime"] = *g.Config.OsakaTime
		}
	}

	// Cancun introduced blobs; besu requires a blobSchedule for any
	// chain with cancunTime active. Conservative defaults — match
	// mainnet activation params (target=3, max=6, baseFeeUpdateFraction=
	// 3338477) for Cancun; Prague/Osaka tweak target/max via BPO EIPs
	// (7691). State-actor's e2e suite doesn't actually use blob txs
	// (spamoor's erc20_bloater is regular calldata), but besu refuses
	// to boot a Cancun-active chainspec without a blobSchedule.
	if g.Config != nil && g.Config.CancunTime != nil {
		blobSchedule := map[string]any{
			"cancun": map[string]any{
				"target":                 3,
				"max":                    6,
				"baseFeeUpdateFraction":  3338477,
			},
		}
		if g.Config.PragueTime != nil {
			blobSchedule["prague"] = map[string]any{
				"target":                 6,
				"max":                    9,
				"baseFeeUpdateFraction":  5007109,
			}
		}
		if g.Config.OsakaTime != nil {
			blobSchedule["osaka"] = map[string]any{
				"target":                 9,
				"max":                    12,
				"baseFeeUpdateFraction":  5007109,
			}
		}
		cfg["blobSchedule"] = blobSchedule
	}

	spec := map[string]any{
		"config":     cfg,
		"nonce":      fmt.Sprintf("0x%x", uint64(g.Nonce)),
		"timestamp":  fmt.Sprintf("0x%x", uint64(g.Timestamp)),
		"extraData":  extraDataHex,
		"gasLimit":   fmt.Sprintf("0x%x", gasLimit),
		"difficulty": diffHex,
		"mixHash":    "0x0000000000000000000000000000000000000000000000000000000000000000",
		"coinbase":   "0x0000000000000000000000000000000000000000",
		"alloc":      map[string]any{},
	}
	if baseFeeHex != "" {
		spec["baseFeePerGas"] = baseFeeHex
	}

	out, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return "", fmt.Errorf("besu writeChainSpec marshal: %w", err)
	}
	outPath := filepath.Join(dbPath, ChainSpecFileName)
	if err := os.WriteFile(outPath, append(out, '\n'), 0o644); err != nil {
		return "", fmt.Errorf("besu writeChainSpec write: %w", err)
	}
	return outPath, nil
}

func bytesToHex(b []byte) string {
	const hex = "0123456789abcdef"
	out := make([]byte, len(b)*2)
	for i, c := range b {
		out[i*2] = hex[c>>4]
		out[i*2+1] = hex[c&0x0F]
	}
	return string(out)
}
