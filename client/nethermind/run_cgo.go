//go:build cgo_neth

// Real Run implementation behind the cgo_neth build tag. Only compiled
// inside the Dockerfile.nethermind build context where librocksdb-dev
// and grocksdb are available.
//
// Phase A (this commit): empty-alloc genesis only. State and Code DBs
// stay empty; the 5 metadata DBs (blocks, headers, blockNumbers,
// blockInfos, receipts) get block 0 entries with WasProcessed=true so
// Nethermind's BlockTree boot detection skips its own loader.
//
// Phase B (next commit on this branch): wire entitygen.Source
// → internal/neth/trie.Builder → grocksdb writes for State/Code DBs so
// the writer scales to a multi-million-account devnet.

package nethermind

import (
	"context"
	"errors"
	"fmt"
	"log"

	"github.com/ethereum/go-ethereum/common"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/genesis"
)

// runImpl orchestrates a Nethermind RocksDB write.
//
// Phase A pipeline:
//  1. Resolve genesis fields (chain ID, gasLimit, extraData, timestamp)
//     from cfg.* / --genesis path.
//  2. Open 7 grocksdb instances under cfg.DBPath/db/.
//  3. Build the empty-alloc genesis header (state root = EmptyTreeHash).
//  4. Write the genesis row across blocks/, headers/, blockNumbers/,
//     blockInfos/ (with WasProcessed=true), receipts/Blocks CF.
//  5. Close cleanly. Return Stats with the computed state root and the
//     genesis block hash for visibility.
func runImpl(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	_ = ctx
	_ = opts

	if cfg.TrieMode == generator.TrieModeBinary {
		return nil, errors.New("nethermind doesn't support binary trie (EIP-7864)")
	}
	if cfg.DBPath == "" {
		return nil, errors.New("--db is required for --client=nethermind")
	}

	// Phase A: only empty-alloc supported. Any non-zero account/contract
	// count means the user expects state-trie writes that Phase B implements.
	if cfg.NumAccounts > 0 || cfg.NumContracts > 0 || cfg.DeepBranch.Enabled() {
		return nil, errors.New(
			"--client=nethermind: Phase A only supports empty alloc " +
				"(--accounts=0 --contracts=0). Phase B with full entitygen " +
				"iteration ships in the next commit",
		)
	}
	if len(cfg.GenesisAccounts) > 0 {
		return nil, errors.New(
			"--client=nethermind: Phase A doesn't yet write genesis-alloc " +
				"accounts to state — Phase B's entitygen integration handles them",
		)
	}

	// Pull genesis fields. If the caller passed --genesis, use those values;
	// otherwise default to a dev-mode-ish minimal genesis (chainId 1337,
	// gasLimit 30M, empty extraData, timestamp 0).
	chainID := int64(1337)
	gasLimit := uint64(30_000_000)
	var extraData []byte
	var timestamp uint64
	// If state-actor was started with --genesis, GenesisFilePath is the path.
	// We read it via genesis.LoadGenesis to extract chain ID + gasLimit.
	// For Phase A's empty-alloc default this is optional.
	if GenesisFilePath != "" {
		g, err := genesis.LoadGenesis(GenesisFilePath)
		if err != nil {
			return nil, fmt.Errorf("load genesis: %w", err)
		}
		if g.Config != nil && g.Config.ChainID != nil {
			chainID = g.Config.ChainID.Int64()
		}
		if g.GasLimit != 0 {
			gasLimit = uint64(g.GasLimit)
		}
		if len(g.ExtraData) > 0 {
			extraData = g.ExtraData
		}
		timestamp = uint64(g.Timestamp)
	}
	if ChainIDOverride != 0 {
		chainID = ChainIDOverride
	}
	_ = chainID // Phase A: chainID lives in the chainspec, not the header

	dbs, err := openNethDBs(cfg.DBPath)
	if err != nil {
		return nil, fmt.Errorf("open Nethermind DBs: %w", err)
	}
	defer dbs.Close()

	header := buildEmptyAllocGenesisHeader(chainID, gasLimit, extraData, timestamp)

	hash, err := writeGenesisBlockToDBs(dbs, header)
	if err != nil {
		return nil, fmt.Errorf("write genesis: %w", err)
	}

	if cfg.Verbose {
		log.Printf("nethermind: genesis hash = %s", hash.Hex())
		log.Printf("nethermind: state root  = %s (empty tree)", header.Root.Hex())
		log.Printf("nethermind: 7 RocksDBs written under %s/db/", cfg.DBPath)
	}

	return &generator.Stats{
		StateRoot: header.Root,
		// Other Stats fields (counts, byte totals) stay zero — Phase A
		// writes the 7-DB scaffold only, not synthetic state.
	}, nil
}

// GenesisFilePath / ChainIDOverride are declared in run.go (no build
// tag) so main.go's assignments compile in both build modes. The cgo
// build path reads them from runImpl above; the stub path ignores them.
//
// The blank import is here to keep the common package dependency real
// in the cgo build — header construction in genesis_cgo.go uses it.
var _ = common.Hash{}
