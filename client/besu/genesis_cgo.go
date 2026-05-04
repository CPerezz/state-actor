//go:build cgo_besu

package besu

import (
	"fmt"
	"math/big"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/types"
	gethrlp "github.com/ethereum/go-ethereum/rlp"

	"github.com/nerolation/state-actor/internal/besu/keys"
)

// writeGenesisBlock persists the genesis block + canonical-hash mapping +
// total-difficulty + chain head pointer.
//
// Per Besu's BLOCKCHAIN CF layout (KeyValueStoragePrefixedKeyBlockchainStorage.java:64-70):
//
//	BLOCKCHAIN[0x02 ++ blockHash]    = RLP(header)
//	BLOCKCHAIN[0x03 ++ blockHash]    = RLP(body) — empty txs/ommers/withdrawals
//	BLOCKCHAIN[0x04 ++ blockHash]    = RLP(receipts) — 0xC0 (empty list)
//	BLOCKCHAIN[0x05 ++ UInt256(0)]   = blockHash (canonical num→hash)
//	BLOCKCHAIN[0x06 ++ blockHash]    = totalDifficulty (Bytes32)
//
// Then VARIABLES["chainHeadHash"] = blockHash (32 bytes).
//
// # Write order: chainHeadHash LAST (mirror nethermind e4722af)
//
// Besu's DefaultBlockchain.setGenesis (DefaultBlockchain.java:1027-1057)
// reads VARIABLES["chainHeadHash"] on boot. If empty, Besu writes the
// genesis itself from --genesis-file. If present and matching the
// recomputed genesis hash, Besu skips writeStateTo and starts. If present
// but mismatched, Besu throws InvalidConfigurationException.
//
// We write the chainHeadHash LAST so that a partial run leaves the BLOCKCHAIN
// rows orphan but the boot gate (chainHeadHash) absent. Combined with the
// fresh-dir precondition, this ensures the DB is either fully-bootable or
// not bootable — never silently mis-bootable.
//
// The chainHeadHash write is sync (putSync) so that a power loss after
// the function returns doesn't strand the DB in a half-durable state.
func (b *besuDB) writeGenesisBlock(header *types.Header, totalDifficulty *big.Int) error {
	blockHash := header.Hash()

	// Encode every payload up-front so encoder errors don't leave half-
	// written rows.
	headerRLP, err := gethrlp.EncodeToBytes(header)
	if err != nil {
		return fmt.Errorf("besu: encode header: %w", err)
	}

	// Body RLP layout per types.Block.EncodeRLP:
	//   RLP_LIST [ transactions, ommers ]   (legacy)
	//   RLP_LIST [ transactions, ommers, withdrawals ]   (Shanghai+)
	//
	// For genesis, all three lists are empty. We use legacy 2-tuple shape
	// because v1 supports through Shanghai but the genesis block itself
	// doesn't need withdrawals (those land starting at the Shanghai fork
	// block, which is post-genesis on a custom dev chain). If the operator
	// configures shanghaiTime=0 in the genesis JSON, Besu adds an empty
	// withdrawals list to the body RLP automatically on its first read of
	// the block — but the GENESIS body is always 2-tuple per
	// GenesisState.buildGenesisBlock at GenesisState.java:201-233.
	bodyRLP, err := gethrlp.EncodeToBytes(struct {
		Txs    []*types.Transaction
		Ommers []*types.Header
	}{
		Txs:    []*types.Transaction{},
		Ommers: []*types.Header{},
	})
	if err != nil {
		return fmt.Errorf("besu: encode body: %w", err)
	}

	// Receipts: empty RLP list = single byte 0xC0. Direct constant write
	// (NOT RLP.encodeOne([]byte{0xC0}) which would double-wrap).
	receiptsRLP := []byte{0xC0}

	// Total difficulty as Bytes32 (32-byte big-endian).
	tdHash := common.BigToHash(totalDifficulty)

	// Now perform the writes via the besuDB.put fast path. All targets are
	// in BLOCKCHAIN CF except the final chainHeadHash which goes to
	// VARIABLES.
	if err := b.put(cfIdxBlockchain, keys.BlockHeaderKey(blockHash), headerRLP); err != nil {
		return fmt.Errorf("besu: write header: %w", err)
	}
	if err := b.put(cfIdxBlockchain, keys.BlockBodyKey(blockHash), bodyRLP); err != nil {
		return fmt.Errorf("besu: write body: %w", err)
	}
	if err := b.put(cfIdxBlockchain, keys.BlockReceiptsKey(blockHash), receiptsRLP); err != nil {
		return fmt.Errorf("besu: write receipts: %w", err)
	}
	if err := b.put(cfIdxBlockchain, keys.CanonicalHashKey(0), blockHash[:]); err != nil {
		return fmt.Errorf("besu: write canonical hash: %w", err)
	}
	if err := b.put(cfIdxBlockchain, keys.TotalDifficultyKey(blockHash), tdHash[:]); err != nil {
		return fmt.Errorf("besu: write total difficulty: %w", err)
	}

	// LAST: chainHeadHash (with sync). This is the boot gate.
	if err := b.putSync(cfIdxVariables, keys.ChainHeadHashKey, blockHash[:]); err != nil {
		return fmt.Errorf("besu: write chainHeadHash: %w", err)
	}

	return nil
}

// supportedFork returns nil if the genesis JSON's config block targets a
// fork through Shanghai. Cancun+ configs (cancunTime / pragueTime /
// shanghaiTime > 0 with subsequent forks) are rejected with a clear error so
// users don't get a silent "wrong genesis hash" boot failure. Cancun+ support
// is a possible future addition.
func supportedFork(g *genesisJSONConfig) error {
	if g == nil {
		return nil
	}
	// Shanghai is allowed (we just don't add a withdrawals list to the
	// genesis body — see writeGenesisBlock). Cancun+ adds blob fields
	// (excessBlobGas, blobGasUsed, parentBeaconBlockRoot) to the header
	// that we'd need to plumb through.
	if g.CancunTime != nil {
		return fmt.Errorf("besu v1 supports through Shanghai; Cancun config (cancunTime=%d) requires v2 follow-up", *g.CancunTime)
	}
	if g.PragueTime != nil {
		return fmt.Errorf("besu v1 supports through Shanghai; Prague config (pragueTime=%d) requires v2 follow-up", *g.PragueTime)
	}
	return nil
}

// genesisJSONConfig is a minimal subset of the genesis JSON `config` block
// we need to detect unsupported fork activation. Larger fields (chainId,
// homesteadBlock, etc.) are loaded by the upstream genesis package; we
// only inspect post-Shanghai timestamps here.
type genesisJSONConfig struct {
	CancunTime *uint64 `json:"cancunTime,omitempty"`
	PragueTime *uint64 `json:"pragueTime,omitempty"`
}
