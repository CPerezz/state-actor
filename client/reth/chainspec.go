package reth

import (
	"bufio"
	"encoding/hex"
	"encoding/json"
	"fmt"
	"os"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/genesis"
)

// writeChainSpec streams a Reth-compatible chainspec JSON to outPath with
// EVERY generated account embedded in its alloc. Reth's `init` computes the
// genesis state root from alloc and writes the resulting state + genesis
// block into the DB. This avoids the state-root-validation mismatch that
// `init-state` raises when the dump's declared root differs from the root
// computed off the chainspec.
//
// The alloc is STREAMED (bufio.Writer, one account per JSON entry) rather
// than materialized in memory first, so the chainspec's JSON can grow to
// arbitrary size without proportional RAM on our side.
//
// When genesisPath is non-empty, every top-level field from that file EXCEPT
// alloc is copied into the chainspec; chainID overrides config.chainId when
// non-zero.
func writeChainSpec(
	genesisPath, outPath string,
	chainID int64,
	allocFn func(out *bufio.Writer) error,
) error {
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

	delete(spec, "alloc")
	top, err := json.MarshalIndent(spec, "", "  ")
	if err != nil {
		return fmt.Errorf("marshal chainspec: %w", err)
	}
	if len(top) < 2 || top[len(top)-1] != '}' {
		return fmt.Errorf("unexpected chainspec top shape")
	}

	f, err := os.Create(outPath)
	if err != nil {
		return fmt.Errorf("create chainspec file: %w", err)
	}
	defer f.Close()
	bw := bufio.NewWriterSize(f, 1<<20)

	if _, err := bw.Write(top[:len(top)-1]); err != nil {
		return err
	}
	if _, err := bw.WriteString(",\n  \"alloc\": {"); err != nil {
		return err
	}
	if allocFn != nil {
		if err := allocFn(bw); err != nil {
			return err
		}
	}
	if _, err := bw.WriteString("\n  }\n}\n"); err != nil {
		return err
	}
	return bw.Flush()
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

// writeAllocEntry writes one `"0xaddr": {balance, nonce, code, storage}`
// entry to w. sep is "" for the first entry and "," for subsequent ones —
// the caller owns separator tracking because alloc is streamed from a
// source where "first/not-first" is known outside this helper.
//
// Matches alloy_genesis::GenesisAccount's serde layout; fields skip when
// zero/empty to mirror serde's skip_serializing_if.
func writeAllocEntry(w *bufio.Writer, sep string, ad *accountData) error {
	if _, err := w.WriteString(sep); err != nil {
		return err
	}
	if _, err := w.WriteString("\n    \"0x"); err != nil {
		return err
	}
	if _, err := w.WriteString(hex.EncodeToString(ad.address[:])); err != nil {
		return err
	}
	if _, err := w.WriteString(`": {"balance":"`); err != nil {
		return err
	}
	if _, err := w.WriteString(ad.account.Balance.Hex()); err != nil {
		return err
	}
	if err := w.WriteByte('"'); err != nil {
		return err
	}
	if ad.account.Nonce > 0 {
		if _, err := fmt.Fprintf(w, `,"nonce":"0x%x"`, ad.account.Nonce); err != nil {
			return err
		}
	}
	if len(ad.code) > 0 {
		if _, err := w.WriteString(`,"code":"0x`); err != nil {
			return err
		}
		if _, err := w.WriteString(hex.EncodeToString(ad.code)); err != nil {
			return err
		}
		if err := w.WriteByte('"'); err != nil {
			return err
		}
	}
	if len(ad.storage) > 0 {
		if _, err := w.WriteString(`,"storage":{`); err != nil {
			return err
		}
		for i, s := range ad.storage {
			if i > 0 {
				if err := w.WriteByte(','); err != nil {
					return err
				}
			}
			if _, err := w.WriteString(`"0x`); err != nil {
				return err
			}
			if _, err := w.WriteString(hex.EncodeToString(s.Key[:])); err != nil {
				return err
			}
			if _, err := w.WriteString(`":"0x`); err != nil {
				return err
			}
			if _, err := w.WriteString(hex.EncodeToString(s.Value[:])); err != nil {
				return err
			}
			if err := w.WriteByte('"'); err != nil {
				return err
			}
		}
		if err := w.WriteByte('}'); err != nil {
			return err
		}
	}
	return w.WriteByte('}')
}

// streamAlloc walks the deterministic entity stream and writes each account
// as a chainspec alloc entry. Storage roots are finalized inline for
// contracts. stats is updated with counts as accounts are emitted.
//
// Memory footprint is bounded by one account's storage map (contracts with
// millions of slots DO buffer all slots at once; see nextContract). The
// chainspec JSON itself is written incrementally.
func streamAlloc(cfg generator.Config, w *bufio.Writer, stats *generator.Stats) error {
	src := newEntitySource(cfg)
	first := true
	emit := func(ad *accountData, kind string) error {
		sep := ","
		if first {
			sep = ""
			first = false
		}
		if err := writeAllocEntry(w, sep, ad); err != nil {
			return fmt.Errorf("alloc entry %s (%s): %w", ad.address.Hex(), kind, err)
		}
		return nil
	}

	for _, ad := range src.genesisAccounts() {
		if err := finalizeStorageRoot(ad); err != nil {
			return err
		}
		if err := emit(ad, "genesis"); err != nil {
			return err
		}
		if len(ad.code) > 0 || len(ad.storage) > 0 {
			stats.ContractsCreated++
		} else {
			stats.AccountsCreated++
		}
		stats.StorageSlotsCreated += len(ad.storage)
	}
	for _, ad := range src.injectedAccounts() {
		if err := emit(ad, "inject"); err != nil {
			return err
		}
		stats.AccountsCreated++
	}
	for i := 0; i < cfg.NumAccounts; i++ {
		ad := src.nextEOA()
		if err := emit(ad, "eoa"); err != nil {
			return err
		}
		stats.AccountsCreated++
		if len(stats.SampleEOAs) < 3 {
			stats.SampleEOAs = append(stats.SampleEOAs, ad.address)
		}
	}
	for i := 0; i < cfg.NumContracts; i++ {
		numSlots := src.nextSlotCount()
		ad := src.nextContract(numSlots)
		if err := finalizeStorageRoot(ad); err != nil {
			return err
		}
		if err := emit(ad, "contract"); err != nil {
			return err
		}
		stats.ContractsCreated++
		stats.StorageSlotsCreated += numSlots
		if len(stats.SampleContracts) < 3 {
			stats.SampleContracts = append(stats.SampleContracts, ad.address)
		}
	}
	return nil
}
