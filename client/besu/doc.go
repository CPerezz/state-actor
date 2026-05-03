// Package besu writes a fully-bootable Hyperledger Besu 26.5.0 RocksDB
// database directly via cgo+grocksdb, bypassing Besu's GenesisState
// in-memory recompute on first boot.
//
// # Approach
//
// Open one RocksDB instance under <datadir>/database/ with the 8 column
// families Besu Bonsai expects (default + BLOCKCHAIN + ACCOUNT_INFO_STATE +
// CODE_STORAGE + ACCOUNT_STORAGE_STORAGE + TRIE_BRANCH_STORAGE +
// TRIE_LOG_STORAGE + VARIABLES). Stream synthetic accounts through a temp
// Pebble DB (Phase 1), iterate them in addrHash-sorted order to feed the
// Bonsai path-keyed trie builder (Phase 2), write per-account flat state +
// per-account storage trie nodes via NodeSink, then assemble the genesis
// block (header / body / receipts / canonical hash / TD), and finally write
// VARIABLES["chainHeadHash"] last.
//
// The result boots `hyperledger/besu:26.5.0` against the produced datadir
// without any flags — Besu reads the chainHeadHash, validates the stored
// genesis block, and starts from block 0 ready.
//
// # Build
//
// state-actor's Besu path is **Docker-only**. The cgo_besu build tag gates
// all grocksdb-importing files; vanilla `go build` (the local default)
// compiles the stub at run_stub.go which returns a clear error directing
// the user at the Dockerfile.
//
//	docker build -f Dockerfile.besu -t state-actor-besu .
//	docker run --rm -v $PWD/_artifacts:/data state-actor-besu \
//	  --client=besu --db=/data --accounts=1000 --seed=42
//
// # Pinned target
//
// internal/besu/'s RLP shapes, key encodings, and trie/builder.go's
// Bonsai path-keyed MPT mirror Besu upstream tag 26.5.0 (May 2026). End-to-end
// smoke and the Tier 2 differential oracle run against the released image
// hyperledger/besu:26.5.0 — the boot contract Besu enforces (chainHeadHash
// pointer, genesis block hash match, Bonsai trie path-keyed encoding) is
// stable across the released line.
package besu
