package geth

import (
	"context"
	"iter"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"
	"github.com/ethereum/go-ethereum/core/rawdb"
	"github.com/ethereum/go-ethereum/core/types"
	"github.com/ethereum/go-ethereum/crypto"
	"github.com/holiman/uint256"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/templates"
)

// TestSpecStorageTrieNodesPersisted pins that spec-Phase-0's StackTrie
// emits per-node writes under TrieNodeStoragePrefix + addrHash. Before
// the fix, the Phase 0 builder used trie.NewStackTrie(nil) — no
// callback — so the storage trie root was computed in RAM but no
// trie-node rows landed in PathDB. eth_call (snapshot fast-path) kept
// working, hiding the regression in CI; eth_getProof, snapshot
// regeneration, and any storage-trie walk would have returned wrong
// results for spec entities.
//
// The synthetic-contract Phase 2 path (buildStorageTrie) always
// installed the callback. This test exercises the spec-entity Phase 0
// path specifically by running with NumContracts=0.
func TestSpecStorageTrieNodesPersisted(t *testing.T) {
	addr := common.HexToAddress("0x000000000000000000000000000000000000abcd")
	addrHash := crypto.Keccak256Hash(addr[:])

	storage := map[common.Hash]common.Hash{
		common.HexToHash("0x01"): common.HexToHash("0xaa"),
		common.HexToHash("0x02"): common.HexToHash("0xbb"),
		common.HexToHash("0x03"): common.HexToHash("0xcc"),
		common.HexToHash("0x04"): common.HexToHash("0xdd"),
		common.HexToHash("0x05"): common.HexToHash("0xee"),
		common.HexToHash("0x06"): common.HexToHash("0xff"),
		common.HexToHash("0x07"): common.HexToHash("0x11"),
		common.HexToHash("0x08"): common.HexToHash("0x22"),
		common.HexToHash("0x09"): common.HexToHash("0x33"),
		common.HexToHash("0x0a"): common.HexToHash("0x44"),
	}

	cfg := generator.Config{
		DBPath:         filepath.Join(t.TempDir(), "geth", "chaindata"),
		NumAccounts:    0,
		NumContracts:   0,
		TrieMode:       generator.TrieModeMPT,
		WriteTrieNodes: true,
		PreAlloc: []templates.PreAllocEntity{{
			Address: addr,
			Account: &types.StateAccount{
				Nonce:    1,
				Balance:  uint256.NewInt(1_000_000),
				Root:     types.EmptyRootHash,
				CodeHash: types.EmptyCodeHash[:],
			},
			Storage: storageIterFromMap(storage),
		}},
	}

	stats, err := Populate(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("Populate: %v", err)
	}
	if (stats.StateRoot == common.Hash{}) {
		t.Fatal("state root is zero — Populate produced no state")
	}

	// Reopen the production Pebble DB and scan for TrieNodeStoragePrefix
	// rows for the spec entity's addrHash.
	w, err := NewWriter(cfg.DBPath)
	if err != nil {
		t.Fatalf("reopen DB: %v", err)
	}
	defer w.Close()

	prefix := append([]byte{}, rawdb.TrieNodeStoragePrefix...)
	prefix = append(prefix, addrHash[:]...)

	it := w.DB().NewIterator(prefix, nil)
	defer it.Release()

	count := 0
	for it.Next() {
		count++
	}
	if count == 0 {
		t.Fatalf("no TrieNodeStoragePrefix rows for spec entity addrHash=%s — "+
			"Phase 0 StackTrie callback failed to persist storage trie nodes; "+
			"eth_getProof and snapshot regeneration would fail at runtime",
			addrHash.Hex())
	}
	t.Logf("spec entity %s has %d storage trie node rows persisted", addr.Hex(), count)
}

// storageIterFromMap wraps a map in iter.Seq2[common.Hash, common.Hash]
// for use as PreAllocEntity.Storage. Mirrors helpers in
// generator/prealloc_test.go + client/nethermind (intentionally
// duplicated; cross-package test imports would cycle).
func storageIterFromMap(m map[common.Hash]common.Hash) iter.Seq2[common.Hash, common.Hash] {
	if len(m) == 0 {
		return nil
	}
	return func(yield func(common.Hash, common.Hash) bool) {
		for k, v := range m {
			if !yield(k, v) {
				return
			}
		}
	}
}
