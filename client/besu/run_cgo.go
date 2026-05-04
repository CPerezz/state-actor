//go:build cgo_besu

package besu

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log"
	"math/big"
	"os"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/genesis"
)

// runImpl is the cgo_besu orchestrator. It executes the full pipeline:
//
//  1. Validate config and load --genesis JSON if provided.
//  2. Open RocksDB at <datadir>/database/ (fresh-dir guard fires if not fresh).
//  3. Phase 1 + Phase 2 streaming pipeline → state root + accumulated stats.
//     Trie nodes + flat-state writes go through nodeSink's shared multi-CF
//     WriteBatch (16 MiB flush threshold).
//  4. Build the genesis block header carrying the computed state root.
//  5. NodeSink.SaveWorldState writes worldRoot, worldBlockHash, root-RLP at
//     key=Bytes.EMPTY, then forces a sync flush.
//  6. writeAdvisorySentinels: flatDbStatus=FULL, worldBlockNumber=0.
//  7. writeGenesisBlock: BLOCKCHAIN keys + chainHeadHash LAST (sync write).
//  8. WriteDatabaseMetadata: <datadir>/DATABASE_METADATA.json sidecar.
//  9. Close DB.
//
// Step ordering rationale:
//   - DB opens BEFORE metadata. If openBesuDB fails (fresh-dir violation),
//     we don't leave an orphan metadata file claiming an intact DB exists.
//   - Metadata writes LAST among on-disk artifacts. Same reason: if any
//     earlier step fails, the DB has data but no metadata sidecar — Besu
//     refuses to open it (StorageException) which is the desired loud-fail
//     semantics for "incomplete state-actor run".
//   - Within step 7, chainHeadHash writes LAST and with sync — it's the
//     boot gate per DefaultBlockchain.setGenesis.
func runImpl(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error) {
	if cfg.DBPath == "" {
		return nil, errors.New("besu: --db is required")
	}

	// Warn-and-ignore --chain-id (mirror nethermind 600a25c). Besu reads
	// chainId from --genesis-file at boot, not from the DB.
	if ChainIDOverride != 0 {
		log.Printf(
			"warning: --chain-id=%d is ignored for --client=besu " +
				"(Besu reads chainId from --genesis-file at boot, not from the DB). " +
				"Make sure --chain-id matches your genesis JSON's config.chainId.",
			ChainIDOverride,
		)
	}

	// Load genesis JSON if provided. Falls back to a minimal dev-default.
	genesisCfg, err := loadOrDefault(GenesisFilePath)
	if err != nil {
		return nil, err
	}

	// Reject Cancun+ configs early. v1 supports through Shanghai.
	if err := supportedFork(&genesisCfg.unstable); err != nil {
		return nil, err
	}

	// Open the DB (with fresh-dir precondition).
	db, err := openBesuDB(cfg.DBPath)
	if err != nil {
		return nil, err
	}
	defer db.Close()

	// Sink shares the single multi-CF WriteBatch with all flat-state writes.
	sink := newNodeSink(db)
	defer sink.Close()

	// Phase 1 + Phase 2.
	rootHash, rootRLP, stats, err := writeStateAndCollectRoot(ctx, cfg, db, sink)
	if err != nil {
		return nil, err
	}

	// Build the genesis header with the computed state root.
	header := buildGenesisHeader(genesisCfg, rootHash)
	blockHash := header.Hash()

	// SaveWorldState: emits the three TRIE_BRANCH_STORAGE sentinels and
	// triggers a sync flush of all pending writes.
	if err := sink.SaveWorldState(blockHash, rootHash, rootRLP); err != nil {
		return nil, err
	}

	// flatDbStatus + worldBlockNumber.
	if err := db.writeAdvisorySentinels(); err != nil {
		return nil, err
	}

	// Genesis block keys (chainHeadHash LAST, sync write).
	if err := db.writeGenesisBlock(header, genesisCfg.difficulty); err != nil {
		return nil, err
	}

	// Sidecar metadata. Last on-disk write so a partial failure leaves the
	// DB without a metadata sidecar (Besu refuses to open).
	if err := WriteDatabaseMetadata(cfg.DBPath); err != nil {
		return nil, err
	}

	// Surface the computed state root in Stats so main.go's print block
	// and live-stats hooks see the real value (not 0x000...).
	stats.StateRoot = rootHash
	return stats, nil
}

// loadOrDefault loads a genesis JSON from path (if non-empty) or returns
// a minimal dev-default. The default chainId is 1337 with a London-frozen
// fork schedule (londonBlock at Long.MAX so genesis is pre-London and we
// don't need baseFeePerGas in the header).
type besuGenesis struct {
	chainID    *big.Int
	gasLimit   uint64
	difficulty *big.Int
	timestamp  uint64
	extraData  []byte
	coinbase   common.Address
	mixHash    common.Hash
	parentHash common.Hash
	nonce      uint64
	baseFee    *big.Int // nil if pre-London; *big.Int if londonBlock <= 0
	unstable   genesisJSONConfig
}

func loadOrDefault(path string) (*besuGenesis, error) {
	if path == "" {
		return &besuGenesis{
			chainID:    big.NewInt(1337),
			gasLimit:   30_000_000,
			difficulty: big.NewInt(0),
			timestamp:  0,
		}, nil
	}
	g, err := genesis.LoadGenesis(path)
	if err != nil {
		return nil, fmt.Errorf("besu: load genesis: %w", err)
	}

	// Re-parse the raw JSON to inspect post-Shanghai timestamps that
	// genesis.LoadGenesis doesn't surface in its struct.
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("besu: re-read genesis: %w", err)
	}
	var top struct {
		Config genesisJSONConfig `json:"config"`
	}
	_ = json.Unmarshal(raw, &top) // best-effort; missing fields are nil

	var diff *big.Int
	if g.Difficulty != nil {
		diff = g.Difficulty.ToInt()
	} else {
		diff = big.NewInt(0)
	}
	var chainID *big.Int
	if g.Config != nil {
		chainID = g.Config.ChainID
	} else {
		chainID = big.NewInt(1337)
	}
	var baseFee *big.Int
	if g.BaseFee != nil {
		baseFee = g.BaseFee.ToInt()
	}
	out := &besuGenesis{
		chainID:    chainID,
		gasLimit:   uint64(g.GasLimit),
		difficulty: diff,
		timestamp:  uint64(g.Timestamp),
		extraData:  []byte(g.ExtraData),
		coinbase:   g.Coinbase,
		mixHash:    g.Mixhash,
		parentHash: g.ParentHash,
		nonce:      uint64(g.Nonce),
		baseFee:    baseFee,
		unstable:   top.Config,
	}
	return out, nil
}

// buildGenesisHeader assembles a *types.Header for the genesis block from
// the besuGenesis fields plus the computed state root.
//
// Empty-list / empty-trie fields (TxHash, ReceiptHash, UncleHash, Bloom,
// WithdrawalsHash if Shanghai+) use the canonical Ethereum constants so
// the resulting block hash matches what Besu's GenesisState.buildHeader
// would compute for the same alloc.
func buildGenesisHeader(g *besuGenesis, stateRoot common.Hash) *types.Header {
	// EmptyTrie is the standard Ethereum MPT root for empty txs/receipts.
	emptyTrie := common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	// Empty list keccak — used as the OmmersHash for genesis.
	emptyList := common.HexToHash("0x1dcc4de8dec75d7aab85b567b6ccd41ad312451b948a7413f0a142fd40d49347")

	header := &types.Header{
		ParentHash:  g.parentHash,
		UncleHash:   emptyList,
		Coinbase:    g.coinbase,
		Root:        stateRoot,
		TxHash:      emptyTrie,
		ReceiptHash: emptyTrie,
		Bloom:       types.Bloom{},
		Difficulty:  g.difficulty,
		Number:      big.NewInt(0),
		GasLimit:    g.gasLimit,
		GasUsed:     0,
		Time:        g.timestamp,
		Extra:       g.extraData,
		MixDigest:   g.mixHash,
		Nonce:       types.EncodeNonce(g.nonce),
		BaseFee:     g.baseFee, // nil if pre-London; *big.Int if londonBlock=0
	}
	return header
}
