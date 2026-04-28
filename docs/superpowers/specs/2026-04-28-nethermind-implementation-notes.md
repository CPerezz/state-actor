# Nethermind Client — Implementation Notes (post-PR#3)

**Branch:** `feat/multi-client-nethermind`
**Date:** 2026-04-28
**Status:** Implemented — ready for review
**Companion to:** [`2026-04-24-nethermind-client-design.md`](./2026-04-24-nethermind-client-design.md) — superseded by direct-write decision below.

---

## TL;DR

The 2026-04-24 design picked **Option B: Parity chainspec + ExitOnBlockNumber**. The team moved to **Option A: direct RocksDB write** during planning, and that's what shipped. This file captures the decision, the wire-format gotchas the planning docs got wrong, and the smoke-test evidence that the architecture works at scale.

---

## What changed from the original design

| Aspect | Original spec (Option B) | What shipped (Option A) |
|---|---|---|
| Bootstrap mechanism | Emit Parity chainspec, run `Nethermind.Runner --Init.ExitOnBlockNumber=0` to commit, then re-run for serving | Write the 7 RocksDBs Nethermind reads on boot directly, set `BlockInfos[0].WasProcessed=true` so the loader skips its own genesis pass |
| Cgo | Avoided (run Nethermind as a subprocess) | Required — `linxGnu/grocksdb v1.10.8` against RocksDB 10.10.1 built from source in `Dockerfile.nethermind` |
| Local-build cost | Zero | macOS dev needs Docker; native build behind `-tags cgo_neth` requires librocksdb |
| Scaling ceiling | Bounded by Nethermind's `LoadGenesisBlock` deserializing every alloc into a `Dictionary<Address, ChainSpecAllocation>` (~500 B/account heap → OOM at multi-million scale, upstream issue #7361) | Bounded only by disk: streaming temp Pebble for sorted-by-addrHash account-trie build |
| State-trie writer | N/A (Nethermind builds it from chainspec) | `internal/neth/trie.Builder` wrapping go-ethereum's `StackTrie` with a HalfPath-keyed sink |

The pivot happened because of the chainspec-deserialization scaling cliff. state-actor's reason to exist is multi-million-account devnets; Option B couldn't get there.

---

## Wire-format corrections (planning docs were wrong)

### 1. `blockNumbers` value is fixed-width 8 bytes BE — NOT no-leading-zeros

Both the deep-feature-planning CCD and the code comments inside `client/nethermind/genesis_cgo.go` mirrored `Int64Extensions.ToBigEndianSpanWithoutLeadingZeros` (1 byte for genesis). That's the **key** format for `blockInfos/`, but the `blockNumbers/` **value** is 8-byte fixed-width.

Source: `Nethermind.Blockchain.Headers.HeaderStore.GetBlockNumberFromBlockNumberDb` at upstream/master:09bd5a2d, line 103:

```csharp
if (numberSpan.Length != 8)
{
    throw new InvalidDataException($"Unexpected number span length: {numberSpan.Length}");
}
```

Symptom of getting this wrong: Nethermind's BlockTree initializer throws `InvalidDataException("Unexpected number span length: 1")` and falls back to chainspec genesis, silently ignoring the on-disk DB.

Fix in `genesis_cgo.go` writes 8-byte BE for the blockNumbers value while keeping the no-leading-zeros encoding for the blockInfos key.

### 2. `BaseDbPath` is the data root — NOT a parent of the data root

Geth's convention: data dir = `<base>/geth/chaindata/`. state-actor's geth path mirrors this with a `db/` subdir.

Nethermind's convention: data dir = `<base>/<dbName>/`, no intermediate. So if state-actor writes to `<dbPath>/db/state/`, Nethermind needs `BaseDbPath=<dbPath>/db`, not `BaseDbPath=<dbPath>`.

Symptom: Nethermind opens an empty DB at `<base>/blockInfos/` (which it auto-creates), sees `last_sequence=0`, falls back to chainspec genesis. No error message — your DB is just bypassed.

**Fixed:** state-actor's Nethermind writer now drops the `db/` subdir, so `--db=<path>` and Nethermind's `BaseDbPath=<path>` line up 1:1. The smoke-test config is updated accordingly.

### 3. `stateDBSink.SetStorageNode` is required — not a stub

The original `genesis_alloc_cgo.go` shipped with `SetStorageNode` returning `errors.New("storage trie not supported in Phase B genesis-alloc path")` because the genesis-alloc path didn't write storage. When Phase B's synthetic accounts started exercising contract storage, that placeholder error fired immediately.

Fix: implement `SetStorageNode` to write at the HalfPath storage key (`section=2 + addrHash + path[:8] + pathLen + keccak`).

---

## What ships in PR#3

| Commit | Scope |
|---|---|
| `154a6a6` refactor: introduce Writer interface and extract `client/geth/` | PR#1 prerequisite |
| `76a00d6` refactor(generator): extract RNG primitives into `internal/entitygen/` | PR#2 prerequisite |
| `96ce2cc` foundation encoders — keccak constants, HexPrefix, HalfPath, Account RLP | B3a |
| `7357a0c` block-tree encoders + TrieNode + vendored fixtures | B3b |
| `9ebafe2` `internal/neth/trie.Builder` + `NodeStorage` callback wrapping geth StackTrie | B4 |
| `6803a14` `client/nethermind/` scaffold + `--client=nethermind` CLI dispatch | B5 scaffold |
| `32cecb3` cgo build-tag split + `Dockerfile.nethermind` (RocksDB-from-source) | B5 build infra |
| `edba259` Phase A genesis-only RocksDB writer (empty alloc) | B5 step 1 |
| `6508dad` `blockNumbers` 8-byte BE fix + Phase B genesis-alloc state writes | B5 step 2 + bug fix |
| `6408662` Phase B synthetic-account state writes via entitygen + temp Pebble | B5 final |

## Pipeline summary

```
--accounts=N    --contracts=M    --genesis=g.json
       │              │                │
       └──────┬───────┴────────────────┘
              ▼
      writeSyntheticAccounts
              │
              ├── Phase 1 (random-order):
              │   ├── EOAs    → entitygen.GenerateEOA  → temp Pebble
              │   ├── contracts → entitygen.GenerateContract
              │   │       ├── per-slot: Builder.AddStorageSlot   ← writes State DB at HalfPath storage keys
              │   │       └── FinalizeStorageRoot                ← computes storage root
              │   └── code → Code DB at keccak(code)
              │
              └── Phase 2 (addrHash-sorted via Pebble LSM):
                  └── for each StateAccount:
                      ├── encode as Nethermind RLP
                      └── Builder.AddAccount                     ← writes State DB at HalfPath state keys
                  └── FinalizeStateRoot                          ← global state root
                          │
                          ▼
              header.Root = stateRoot
                          │
                          ▼
              writeGenesisBlockToDBs (the existing Phase A pipeline)
                          ├── headers/      composite key → RLP(header)
                          ├── blocks/       composite key → RLP(block)
                          ├── blockNumbers/ hash(32)      → 8-byte BE  ← FIXED
                          ├── blockInfos/   numKey        → ChainLevelInfo{WasProcessed=true}
                          └── receipts/Blocks CF: composite key → 0xc0
```

Memory: `O(max_slots_per_contract)`. Total entity count is bounded only by the temp Pebble's disk space.

---

## Smoke-test evidence

**Phase A (empty alloc):**
- `state-actor --client=nethermind --db=/data --accounts=0 --contracts=0` → 7-DB datadir.
- Boot `nethermind/nethermind:1.37.0` → genesis hash matches state-actor's reported hash.

**Phase B genesis-alloc + 100 txs:**
- 3 dev wallets pre-funded via `genesis-funded.json`.
- All 100 dev-mode txs land; chain reaches block 100.

**Phase B synthetic accounts:**
- 100 EOAs + 10 contracts: state root deterministic across re-runs (same `--seed`).
- 100K EOAs + 10K contracts: state root reported by state-actor byte-equals what Nethermind reports for `eth_getBlockByNumber("0x0").stateRoot`.
- 1M EOAs + 100K contracts (max-slots=2048, power-law): 67s generation, 835 MB datadir.
- 50 GB stress test: in progress at time of writing — projected ~2 hours at observed 7-8 MB/s sustained throughput.

---

## Known gaps (not blocking PR#3)

- **B6 differential oracle** — the 3 CCD-cited golden hashes from `Nethermind.Blockchain.Test.GenesisBuilderTests` (`empty_accounts_and_storages`, `empty_accounts_and_codes`, `hive_zero_balance_test`). Vendored as Parity-format JSON in `internal/neth/testdata/`; running them needs either a Parity chainspec parser or hand-converted geth-format equivalents. Tracked as the last open task; not on the critical path.
- **Streaming snapshot writes** — the geth path uses an async snapshot-writer goroutine; the Nethermind path is single-goroutine. At 5M+500K scale we observe 30% single-core utilization, so there's headroom to parallelize when needed.
