# Nethermind Client Integration — Design Document

**Branch:** `feat/multi-client-nethermind`
**Date:** 2026-04-24
**Status:** Draft — pending user approval
**Mirrors:** `fork/feat/multi-client-reth` (reth integration, not yet merged to main)

---

## 1. Goal

Add a `client/nethermind/` package and a `--client=nethermind` dispatch branch to `main.go` so `state-actor` can produce a Nethermind-bootable data directory deterministically from the same `generator.Config` used by the geth and reth paths.

Acceptance criteria:
- `state-actor --client=nethermind --db <dir> --accounts 10000 --contracts 500 --genesis genesis.json` produces `<dir>` such that `nethermind --data-dir <dir> --Init.ChainSpecPath <dir>/chainspec.json` boots with the expected state.
- Exit non-zero with a clear error on unsupported flag combinations (`--binary-trie`, `--target-size`, `--deep-branch-*`).
- Determinism: same seed → byte-identical chainspec output.
- Unit tests run without the `nethermind` binary installed.

Out of scope for this PR:
- Binary-trie mode (EIP-7864) — Nethermind does not implement it.
- `--target-size` auto-stop — Nethermind receives a completed chainspec; there is no mid-generation stop point.
- Deep-branch storage trie extensions — encoding relies on geth-specific trie node writes.
- Direct RocksDB writes (was considered; rejected — see §4 Alternatives).

---

## 2. Background

### 2.1 state-actor today (feat/multi-client-nethermind, branched off origin/main@e20d296)

- `generator/` produces deterministic Ethereum state (EOAs, contracts, storage slots) from a `generator.Config` seed. `main.go` currently wires it to `generator.GethWriter`, which writes directly into `<db>/geth/chaindata` (Pebble).
- `genesis/` loads a user-supplied geth-format `genesis.json` and produces a genesis block compatible with geth's RLP schema.
- No `client/` directory exists. No `--client` flag exists. Binary-trie, target-size, deep-branch, and geth-specific snapshot/trie-node writers are all layered onto the single geth path.

### 2.2 reth path as template (fork/feat/multi-client-reth)

Seven `client/reth/*.go` files (~1000 LoC) implement a self-contained alloc-streamer for reth:
1. Generate accounts/contracts/storage deterministically from `cfg.Seed` (`entities.go`, 237 lines — RNG duplicated from `generator/` per the multi-client ADR).
2. Stream every account as a `GenesisAccount` entry into a reth-flavored chainspec JSON (`chainspec.go`, ~300 lines; `streamAlloc` in `chainspec.go:229-296`).
3. Compute per-contract storage root with go-ethereum's `StackTrie` (`statedump.go:17-52`).
4. Invoke `reth init --chain <chainspec> --datadir <dir>` (`reth_binary.go`, `populate.go`).

Key design decisions the reth branch already paid for (we must NOT repeat):
- **Self-contained RNG duplication, not shared package.** Verified at `client/reth/entities.go:60-237` — no `generator.Generator` imports. Nethermind must follow.
- **Persist the chainspec inside the datadir** (reth commit `f992249`). The genesis hash is baked into the DB and the spec must match on every boot. Reth originally used a temp file and deleted it; broke reboot. Fix: default path = `<dbPath>/chainspec.json`.
- **Subcommand pivot** (reth commit `3e43a74`): reth originally used `reth init-state <jsonl>` but hit a state-root-mismatch. Pivoted to `reth init --chain <spec>` with alloc. Nethermind's analog is even more different (§3.4).
- **0xEF code-prefix mask** (reth commit `3e43a74`): reth enforces EIP-3541 on genesis alloc, rejecting random code that happens to start with `0xEF`. `entities.go:145-148` masks `code[0] = 0x60` on collision. Nethermind does NOT enforce EIP-3541 at genesis (§3.3.2) — but we keep the mask for reproducibility parity across clients.

### 2.3 Why not direct RocksDB? (decision already made, recorded for completeness)

Inspecting Nethermind's schema (`Nethermind.Db/DbNames.cs`, `Nethermind.Trie/NodeStorage.cs`) showed 19 separate RocksDB instances with HalfPath-encoded trie keys (42 B state / 74 B storage), plus chainspec-driven genesis validation at every boot. Estimated 2500-3500 LoC of Go + cgo-RocksDB bindings + pinned Nethermind version range. Rejected in favor of the chainspec-based approach, which lets Nethermind's own code handle schema drift.

---

## 3. Design

### 3.1 Package layout

Mirrors reth one-for-one. Every file has an analog; see §7 for details.

```
client/nethermind/
├── doc.go                      # package docstring, CLI usage, limitations
├── entities.go                 # self-contained RNG, accountData, entitySource
├── chainspec.go                # Parity-style ChainSpec JSON streamer
├── statedump.go                # finalizeStorageRoot, encodeStorageValue
├── nethermind_binary.go        # PATH lookup, runNethermind invocation
├── populate.go                 # Populate() orchestrator + Options + validateConfig
└── populate_test.go            # 9 unit tests mirroring reth
```

### 3.2 Chainspec format: Parity-style over geth-style (THE KEY PIVOT)

> **Major finding from upstream analysis:** Nethermind's `GethGenesisLoader` (accepting geth-format `genesis.json`) **does not ship in stable 1.37.0 — only on master / unreleased 1.38.x** (PR [#10046](https://github.com/NethermindEth/nethermind/pull/10046), merged 2026-04-01). Pinning state-actor to an unreleased binary means no one can use this until 1.38.0 ships.

Decision: **emit Nethermind-native Parity-style chainspec**, not geth `genesis.json`. This works on every released Nethermind ≥ 1.36 and matches how Nethermind's own shipped configs (`mainnet.json`, `hoodi.json`, `chiado.json`) are written.

Parity chainspec schema (file: `Nethermind.Specs/ChainSpecStyle/Json/ChainSpecJson.cs`):
```json
{
  "name": "state-actor-dev",
  "dataDir": "state-actor-dev",
  "engine": {
    "NethDev": {
      "params": {}
    }
  },
  "params": {
    "chainId": "0x539",
    "networkID": "0x539",
    "gasLimitBoundDivisor": "0x400",
    "maximumExtraDataSize": "0x20",
    "minGasLimit": "0x1388",
    "maxCodeSize": "0x6000",
    "maxCodeSizeTransition": "0x0",
    "homesteadTransition": "0x0",
    "eip150Transition": "0x0",
    "eip155Transition": "0x0",
    "eip158Transition": "0x0",
    "byzantiumTransition": "0x0",
    "constantinopleTransition": "0x0",
    "petersburgTransition": "0x0",
    "istanbulTransition": "0x0",
    "berlinTransition": "0x0",
    "londonTransition": "0x0",
    "eip1559Transition": "0x0",
    "eip3651TransitionTimestamp": "0x0",
    "eip3855TransitionTimestamp": "0x0",
    "eip3860TransitionTimestamp": "0x0",
    "eip4895TransitionTimestamp": "0x0",
    "eip1153TransitionTimestamp": "0x0",
    "eip4844TransitionTimestamp": "0x0",
    "eip4788TransitionTimestamp": "0x0",
    "terminalTotalDifficulty": "0x0"
  },
  "genesis": {
    "seal": { "ethereum": { "nonce": "0x0", "mixHash": "0x00...00" } },
    "difficulty": "0x1",
    "author": "0x00...00",
    "timestamp": "0x0",
    "gasLimit": "0x1c9c380",
    "extraData": "0x",
    "stateRoot": "0x..."         // OPTIONAL - if omitted Nethermind computes from accounts
  },
  "accounts": {
    "0x...": {
      "balance": "0x...",
      "nonce": "0x...",
      "code": "0x...",
      "storage": { "0x...": "0x..." }
    }
  }
}
```

**Engine choice: `NethDev`** — a PoW-like single-node engine with zero difficulty; boots without any consensus validation. This is what `chainspec/spaceneth.json` uses for the SpaceNeth devnet. Confirmed at `src/Nethermind/Nethermind.Runner/configs/spaceneth.json`. Clique is an alternative but requires a validator address; NethDev requires none.

**JSON property-order note:** the `AutoDetectingChainSpecLoader` (master-only) sniffs the first non-`$schema` key to choose Parity vs geth. Since we emit Parity, we can put `"name"` first (Parity identifier). Not load-bearing on 1.37 because AutoDetecting doesn't exist there — plain `ChainSpecLoader` accepts anything Parity-shaped.

### 3.3 The generated-alloc streaming pipeline

```
generator.Config (seed)
  │
  ▼
entitySource (RNG, deterministic order)
  │
  ├─► genesisAccounts()      [from cfg.GenesisAccounts, sorted]
  ├─► injectedAccounts()     [from cfg.InjectAddresses, sorted]
  ├─► nextEOA() × N          [cfg.NumAccounts]
  └─► nextContract() × M     [cfg.NumContracts]
        │
        ▼
  finalizeStorageRoot(ad)    [statedump.go — StackTrie over sorted storage]
        │
        ▼
  writeAccountEntry(bw, ad)  [chainspec.go — emits one "accounts" entry, streamed]
        │
        ▼
  bufio.Writer (bw) → chainspec.json
        │
        ▼
  runNethermind(binPath, chainspec, datadir)
```

Critical invariants:
1. **RNG stays on one goroutine** (`math/rand.Rand` is not thread-safe). Mirrors `generator/generator.go:1237-1306`.
2. **Accounts sorted by address before emission.** Nethermind's `GenesisBuilder.Preallocate` does `OrderBy(a => a.Key)` (`Nethermind.Blockchain/GenesisBuilder.cs:58`), so output order doesn't affect genesis hash — but sorted output makes JSON diffs readable and guarantees determinism even if future Nethermind versions change their iteration order.
3. **Storage slots sorted by keccak256(key)** inside `finalizeStorageRoot` (matches MPT requirement). Reth's `statedump.go:31-36` — copy verbatim.
4. **0xEF code masking retained** (`entities.go` will copy reth's `code[0] == 0xEF → code[0] = 0x60` at generation time). Nethermind doesn't need it, but retention means `--seed X` produces the same contract set across clients — this is a useful property for cross-client reproducibility tests.

### 3.3.1 Account entry RLP/JSON shape

Nethermind's `AccountsJson` deserializer (`Nethermind.Specs/ChainSpecStyle/Json/AllocationJson.cs`) accepts all of:
- `balance` — `"0x..."` or decimal UInt256. Use hex.
- `nonce` — `"0x..."` or integer. Use hex for symmetry.
- `code` — `"0x..."` hex-encoded EVM bytecode.
- `storage` — map `"0x<32-byte-key>": "0x<32-byte-value>"`. Values with leading-zero trim are accepted (`GenesisBuilder.cs:72` trims them anyway; we trim on our side for output size).
- `constructor`, `builtin`, `codeHash` — not used by state-actor.

### 3.3.2 EIP-3541 enforcement at genesis — NOT an issue

Verified at `Nethermind.State/WorldState.cs`: `InsertCode` has no 0xEF prefix check. EIP-3541 is enforced only at EVM CREATE/CREATE2 time. So even post-London chainspecs accept `0xEF`-prefixed alloc code. The reth mask is cosmetic here but retained for cross-client reproducibility (§3.3).

### 3.4 Binary invocation: NO init subcommand, use runtime mode with `Init.ExitOnBlockNumber=0`

Unlike geth (`geth init`) and reth (`reth init`), Nethermind has **no init-only subcommand**. Confirmed by reading `Nethermind.Runner/Program.cs` — there are no subcommand branches, just CLI options.

The cleanest equivalent found in the codebase is `Init.ExitOnBlockNumber=0`:
- `Nethermind.Init/Steps/InitializeBlockTree.cs:42-45` registers an `ExitOnBlockNumberHandler` on `blockTree.BlockAddedToMain`.
- `Nethermind.Blockchain/ExitOnBlockNumberHandler.cs:27` calls `processExitSource.Exit(0)` when the first main-chain block (genesis) is added.
- Combined with `Init.ProcessingEnabled=false`, the processor stops immediately after `GenesisLoader.Load()` commits (`Nethermind.Init/Steps/LoadGenesisBlock.cs:27-31`), and the runner then reaches the exit handler.

**Command we spawn:**
```bash
nethermind \
  --config none \
  --data-dir <cfg.DBPath> \
  --Init.ChainSpecPath <cfg.DBPath>/chainspec.json \
  --Init.DiscoveryEnabled false \
  --Init.PeerManagerEnabled false \
  --Init.ProcessingEnabled false \
  --Init.ExitOnBlockNumber 0 \
  --Sync.NetworkingEnabled false \
  --Sync.SynchronizationEnabled false \
  --JsonRpc.Enabled false \
  --Merge.Enabled false \
  --Network.MaxActivePeers 0 \
  --Blocks.GenesisTimeoutMs 600000 \
  --log info
```

Rationale for each flag is tabulated in Appendix A (from upstream analysis).

**Termination strategy (two-layer):**
1. **Primary:** Wait for the subprocess to exit on its own (self-exit via `Init.ExitOnBlockNumber=0`). Expected exit code: `0`.
2. **Fallback:** If we haven't seen self-exit within `cfg.Blocks.GenesisTimeoutMs + 60s`, scrape stderr for the substring `"Initialization Completed"` (stable banner from `Nethermind.Core/ThisNodeInfo.cs:23`). Send SIGTERM, wait 60s, send SIGKILL if still alive. Log a warning referencing issue [#9066](https://github.com/NethermindEth/nethermind/issues/9066) (multi-CPU Linux SIGTERM-hang regression since 1.32.3).
3. **Post-exit verification:** assert `<datadir>/db/headers/` and `<datadir>/db/blocks/` and `<datadir>/db/blockInfos/` all exist and are non-empty. If any is missing, return an error (Nethermind aborted before writing genesis).

**Why ExitOnBlockNumber is better than the log-scrape+SIGTERM I originally proposed:** the self-exit is idempotent and cleaner — no SIGTERM, no #9066 hang risk, no log-format coupling. Log scrape becomes a pure fallback.

### 3.5 Configuration validation

`validateConfig(cfg generator.Config) error` rejects (with clear error messages):
- `cfg.DBPath == ""`
- `cfg.TrieMode == generator.TrieModeBinary` (EIP-7864 — not implemented in Nethermind)
- `cfg.TargetSize > 0` (no mid-generation stop; Nethermind receives the complete chainspec)
- `cfg.DeepBranch.Enabled()` (encoding relies on geth trie node writer)

All rejected in `main.go` at CLI parse time as well (mirror reth's `main.go:94-115`).

### 3.6 Datadir preparation

`prepareDatadir(cfg.DBPath) error` — mirrors reth's `populate.go:149-176`:
- `os.MkdirAll(cfg.DBPath, 0o755)` — create if missing.
- Refuse to proceed if `<datadir>/db/` exists AND any of `state/`, `blocks/`, `headers/` inside it are non-empty. Surface the exact path in the error.
- Rationale: Nethermind silently retains stale genesis when the chainspec changes (`GenesisLoader.cs:22` — `if (blockTree.Genesis is null)`). Failing fast prevents users shipping a "new" devnet that actually contains last-run's state.

### 3.7 Options type + public API

```go
type Options struct {
    // NethermindBinaryPath overrides PATH lookup.
    NethermindBinaryPath string

    // SkipNethermindInvocation skips the subprocess spawn.
    // Unit tests use this to assert on the chainspec without requiring
    // the binary to be installed.
    SkipNethermindInvocation bool

    // ChainSpecPath, when non-empty, writes the chainspec to this path.
    // When empty, defaults to <cfg.DBPath>/chainspec.json.
    ChainSpecPath string

    // KeepChainSpec leaves the chainspec on disk after success.
    // Default: true for nethermind (Nethermind's boot reads it every time;
    // unlike reth where the chainspec is only needed on init).
    KeepChainSpec bool
}

// Package-level knobs set from main.go — mirrors reth.
var (
    GenesisFilePath string // from --genesis
    ChainIDOverride int64  // from --chain-id
)

// Populate is the entry point for --client=nethermind.
func Populate(ctx context.Context, cfg generator.Config, opts Options) (*generator.Stats, error)
```

The `Populate` signature and behavior match reth's `client/reth/populate.go:58-67` so `main.go` dispatches uniformly.

### 3.8 Stats reporting

Mirrors reth: `*generator.Stats` is returned with:
- `AccountsCreated`, `ContractsCreated`, `StorageSlotsCreated` — populated during alloc streaming.
- `GenerationTime` — wall-clock from `streamAlloc` start to chainspec-write end.
- `DBWriteTime` — wall-clock around the `runNethermind` subprocess call.
- `StateRoot` — left as `common.Hash{}` (zero). Nethermind computes it internally from alloc; reading it back would require opening the RocksDB RO, which we skip for parity with reth.
- `SampleEOAs`, `SampleContracts` — first 10 of each for the post-generation summary print.

---

## 4. Alternatives considered

| Alternative | Reason rejected |
|---|---|
| Direct RocksDB writer (match geth path) | ~3000 LoC, cgo dependency (currently none), Nethermind schema evolves (PR #6499 HalfPath in 1.28), version pinning hell. Rejected in favor of letting Nethermind's own code handle schema. |
| geth-format `genesis.json` alloc (match reth path style) | `GethGenesisLoader` exists only on unreleased Nethermind master / 1.38.x. State-actor would be unusable with every shipped Nethermind. Rejected in favor of Parity-style chainspec. |
| Spawn Nethermind + SIGTERM after log match | Works but depends on log-format stability. Issue #9066 (multi-CPU Linux) makes SIGTERM hang possible. Rejected as primary; retained as fallback (§3.4). |
| Wall-clock timeout + SIGTERM | No readiness signal; large allocs take minutes. Rejected. |
| Hybrid "produce both Parity and geth chainspec, use whichever boot succeeds" | Doubles test surface and implementation work for zero user value in the near term. Rejected. Could be added later as B1 fallback once 1.38.0 is stable. |

---

## 5. Implementation phases

Six commits, each independently reviewable. Total ~1000 LoC of Go + ~350 LoC tests. Effort estimate: 2-3 focused sessions.

### Phase 1 — Package scaffold + RNG entity source
- `client/nethermind/doc.go` (~60 lines — package overview, CLI usage, limitations, engine choice rationale).
- `client/nethermind/entities.go` — copy `client/reth/entities.go` verbatim, change package line, update doc references from "reth" → "nethermind". 0xEF mask comment updated to note "retained for cross-client reproducibility; Nethermind itself does not enforce EIP-3541 on genesis alloc (WorldState.InsertCode)".
- `client/nethermind/statedump.go` — copy `client/reth/statedump.go` verbatim. Keep the `statedump.go` filename for grep parity with reth even though no JSON dump is produced (see reth historical pain point E1).
- Tests: none yet in this phase — tested transitively via Phase 2.
- Commit message: `feat(nethermind): package scaffold + RNG entity source`

### Phase 2 — Parity chainspec writer
- `client/nethermind/chainspec.go` — streaming Parity chainspec writer.
  - `writeChainSpec(genesisPath, outPath string, chainID int64, allocFn func(*bufio.Writer) error) error` — writes all top-level fields, then streams `"accounts": { ... }` via `allocFn`.
  - `buildChainSpec(chainID int64) map[string]any` — returns name/engine/params/genesis defaults for a NethDev dev chain.
  - `deriveChainID(override int64, g *genesis.Genesis) int64` — priority: override > genesis.Config.ChainID > 1337. Identical to reth's `chainspec.go:148-158`.
  - `loadGenesisForNethermind(path string) (*genesis.Genesis, error)` — wraps `genesis.LoadGenesis`.
  - `writeAccountEntry(bw *bufio.Writer, ad *accountData) error` — emits one entry. UInt256 balance hex-encoded, nonce hex, code hex, storage as nested map with 32-byte keys and trimmed-value hex.
  - `streamAlloc(cfg generator.Config, bw *bufio.Writer, stats *generator.Stats) error` — walks `entitySource`, calls `finalizeStorageRoot`, emits entries. Emission order is four sequential iterators: genesis accounts (sorted by address) → injected accounts (sorted) → RNG-generated EOAs → RNG-generated contracts. No global sort across groups — Nethermind re-sorts at load time via `GenesisBuilder.Preallocate`'s `OrderBy(a => a.Key)`, so our only determinism requirement is reproducibility within each group.
- **Pitfall watch:** JSON property order for `"accounts"` — inside the accounts object, Go's `map[string]any` iteration is randomized. Mitigation: keep `"accounts"` as a literal `"{"`/`"}"` framed via `bufio.Writer`, emit entries directly (not via `json.Marshal` on a map). Matches reth's pattern (`chainspec.go:83-97`).
- Commit message: `feat(nethermind): Parity-style chainspec writer`

### Phase 3 — Binary wrapper
- `client/nethermind/nethermind_binary.go` — `findNethermindBinary`, `runNethermind`.
  - `runNethermind(ctx, binPath, chainspecPath, datadir string, timeout time.Duration, verbose bool) error`:
    - Build arg list from §3.4.
    - `exec.CommandContext(ctx, binPath, args...)`.
    - Capture stdout+stderr tee'd to buffers; in verbose mode also forward to os.Stdout/os.Stderr.
    - Wait for process exit with configurable timeout (cfg-derived).
    - If exit code == 0 → success.
    - If exit times out → fallback: scan stderr buffer for `"Initialization Completed"`; if found, SIGTERM, wait 60s, SIGKILL.
    - If exit code != 0 and != "signaled by us" → surface `lastLines(stderr, 20)` like reth's `reth_binary.go:58-62`.
  - `lastLines(s string, n int) string` — verbatim from reth.
- Commit message: `feat(nethermind): binary invocation wrapper`

### Phase 4 — Populate orchestrator
- `client/nethermind/populate.go`:
  - `Options` struct (§3.7).
  - `Populate(ctx, cfg, opts)`:
    1. `validateConfig(cfg)`.
    2. Resolve binary (unless `SkipNethermindInvocation`).
    3. `os.MkdirAll(cfg.DBPath, 0o755)` + `prepareDatadir(cfg.DBPath)`.
    4. Open chainspec file (path: `opts.ChainSpecPath` or default `<cfg.DBPath>/chainspec.json`).
    5. `streamAlloc` into chainspec via `writeChainSpec`.
    6. `runNethermind`.
    7. Verify post-run: `<datadir>/db/headers/`, `/db/blocks/`, `/db/blockInfos/` exist and are non-empty.
    8. Return `*generator.Stats`.
  - `validateConfig` (§3.5).
  - `prepareDatadir` (§3.6).
  - Package vars `GenesisFilePath`, `ChainIDOverride` (§3.7).
- Commit message: `feat(nethermind): Populate orchestrator + Options`

### Phase 5 — main.go dispatch
- Add `"context"` and `"github.com/nerolation/state-actor/client/nethermind"` imports.
- Extend `--client` flag allow-list to `{"geth", "reth", "nethermind"}` (or just `{"geth", "nethermind"}` if reth hasn't merged yet; adapt at merge time).
- **Merge-conflict note with `fork/feat/multi-client-reth`:** both branches modify `main.go` in the same region (flag declaration + switch). If reth merges first, rebase this PR onto the new main and resolve by extending the existing switch case. If this PR merges first, the reth PR will have the mirror-image conflict — adding `case "reth":` to the switch is trivial.
- Add validation block that rejects `--binary-trie`, `--target-size`, `--deep-branch-accounts` for `nethermind` (mirror reth's `main.go:111-124`).
- Add `case "nethermind":` in dispatch switch that sets package vars, calls `nethermind.Populate`, handles error, updates `liveStats.SetStateRoot` (zero — acceptable).
- Commit message: `feat: --client=nethermind CLI flag + dispatch`

### Phase 6 — Tests
- `client/nethermind/populate_test.go` — 9 tests mirroring reth:
  1. `TestValidateConfigRejectsUnsupported` — binary-trie / target-size / deep-branch / empty-DBPath.
  2. `TestDeriveChainID` — override > genesis > default-1337.
  3. `TestWriteChainSpecEmptyAlloc` — well-formed Parity JSON with expected params.
  4. `TestWriteChainSpecWithGenesis` — user genesis top-level merge.
  5. `TestStreamAllocProducesValidJSON` — full round-trip through `json.Unmarshal`.
  6. `TestStreamAllocReproducibility` — same seed → byte-identical output.
  7. `TestPopulateSkipNethermind` — full pipeline with `SkipNethermindInvocation: true`.
  8. `TestPopulateRejectsExistingDatabase` — pre-populated `<datadir>/db/state/` → clean error.
  9. `TestGenesisAccountsIncludedInChainspec` — `--genesis-alloc` accounts appear in chainspec's `accounts` block.
- Optional `populate_integration_test.go` with build tag `nethermind_e2e`, skipped unless `NETHERMIND_BIN` is set. **Skip for v1; add later if desired.** (Reth has no equivalent.)
- Commit message: `test(nethermind): unit tests for chainspec, populate, binary wrapper`

---

## 6. Risk register

| # | Risk | Likelihood | Impact | Mitigation |
|---|---|---|---|---|
| R1 | Nethermind deprecates/renames Parity-chainspec format | Low — it's the canonical path, used by mainnet.json | High | Pin supported version range in `doc.go`; CI test against multiple versions. |
| R2 | `Init.ExitOnBlockNumber=0` semantics change in a future release | Low | Medium | Log-scrape fallback (§3.4) is independent. |
| R3 | SIGTERM hang (issue #9066) on multi-CPU Linux users | Medium | Medium | 60s grace → SIGKILL with warning log. Not a data-loss event since genesis write completes first. |
| R4 | Large alloc (>1M accounts) OOMs Nethermind during JSON deserialize | Medium — confirmed ~500 B/account heap (issue #7361) | High | Set `--Blocks.GenesisTimeoutMs 600000`; document memory expectation in `doc.go`; test with 100k accounts in CI. |
| R5 | Chainspec JSON invalid after our emit → #4523 hang | Low with unit tests | Medium | `TestStreamAllocProducesValidJSON` round-trips through `json.Unmarshal`; `Populate` optionally pre-validates before spawn. |
| R6 | User re-runs `state-actor` on existing datadir, gets stale state silently | Medium — easy user mistake | Medium | `prepareDatadir` hard-rejects existing `<datadir>/db/state/` (§3.6). |
| R7 | Genesis state root differs between Nethermind 1.36 and 1.37 (consensus bug) | Very low | High | No mitigation needed at our layer; surface Nethermind exit code clearly on failure. |
| R8 | 0xEF mask becomes semantically wrong if state-actor switches to post-Osaka activation in default chainspec | Low | Low | Retained mask is always safe (never invalid code); cross-client parity outweighs optimization. |
| R9 | Go's map iteration in `buildChainSpec` produces non-deterministic top-level JSON key order → broken reproducibility tests | Medium | Low | Use a typed struct for top-level chainspec; only use map-streaming inside the `accounts` block. |
| R10 | Nethermind binary not in PATH on CI → integration tests fail by default | Medium | Low | Mirror reth: no integration test in v1, unit tests cover semantics. |
| R11 | Our RNG sequence diverges from reth's because of subtle `math/rand` version change | Low | Medium | `TestStreamAllocReproducibility` pins a golden hash of the chainspec bytes. |

---

## 7. File-by-file checklist

| File | LoC est. | Copy from reth? | New logic | Tests |
|---|---|---|---|---|
| `doc.go` | ~60 | No (rewritten content) | Package overview, engine=NethDev justification, version pin | — |
| `entities.go` | ~237 | Verbatim (1 line: package + doc tweaks) | None | exercised via chainspec tests |
| `statedump.go` | ~71 | Verbatim | None | exercised via chainspec tests |
| `chainspec.go` | ~320 | Structural mirror | Parity schema (params transitions, engine.NethDev, accounts block) | tests #3, #4, #5, #6, #9 |
| `nethermind_binary.go` | ~100 | Structural mirror | ExitOnBlockNumber-based termination, log-scrape fallback | exercised via populate integration (future) |
| `populate.go` | ~210 | Structural mirror | `prepareDatadir` for `<datadir>/db/state`, `validateConfig` | tests #1, #7, #8 |
| `populate_test.go` | ~360 | Structural mirror | Parity-format assertions | — |
| `main.go` | +~30 lines | Match reth diff | Add nethermind case | — |
| `README.md` | +~20 lines | Match reth section | Document `--client=nethermind` usage, version requirement | — |

**Total new code:** ~1400 LoC (of which ~410 LoC tests, ~310 LoC verbatim from reth, ~680 LoC new Nethermind-specific).

---

## 8. Test plan

### 8.1 Unit (run in every PR, no external deps)

All 9 tests from §5 Phase 6 run via `go test ./client/nethermind/...` with no binary in PATH.

Key assertions:
- **Determinism (test #6):** hash the chainspec bytes for `cfg{Seed: 12345, NumAccounts: 5, NumContracts: 3, MaxSlots: 10, MinSlots: 2, CodeSize: 64}` and pin it in the test (analog to `TestStreamAllocReproducibility` in reth's `populate_test.go:222-241`).
- **JSON validity (test #5):** `json.Unmarshal` the output into `map[string]any`, assert `accounts` is a map of 5+3 entries with non-empty `balance`.
- **Rejection messages (test #1):** assert error messages contain the flag name (`--binary-trie`, `--target-size`, etc.).
- **Parity schema (test #3, #4):** assert `engine.NethDev` present, `params.chainId` correct, `accounts` present (empty or populated).

### 8.2 Integration (opt-in via env)

Not shipped in v1. If added later:
- File: `client/nethermind/populate_integration_test.go` with build tag `nethermind_e2e`.
- Gate: `t.Skip` unless `NETHERMIND_BIN` is set.
- Test: run full `Populate`, verify `<datadir>/db/headers/CURRENT` and `<datadir>/db/state/CURRENT` exist and are non-empty after exit.

### 8.3 End-to-end (manual, documented in README)

Commands to run manually before merging:
```bash
# 1. Small devnet
state-actor --client=nethermind --db /tmp/sa-neth --accounts 1000 --contracts 100 \
            --genesis examples/genesis.json --seed 42
nethermind --data-dir /tmp/sa-neth --Init.ChainSpecPath /tmp/sa-neth/chainspec.json \
           --JsonRpc.Enabled true --JsonRpc.Host 0.0.0.0 --JsonRpc.Port 8545 \
           --Init.DiscoveryEnabled false --Network.MaxActivePeers 0 &
sleep 10
cast block-number --rpc-url http://localhost:8545  # expect 0
cast balance 0x00...01 --rpc-url http://localhost:8545  # expect non-zero for known account
```

Result criteria:
- Block number is `0`.
- A known injected account from `--inject-accounts` shows the expected `999999999 ETH`.
- RPC `eth_getCode` returns expected bytecode for a sampled contract.
- `ps` confirms Nethermind is alive and idle (no crash loop).

### 8.4 Regression guards

- Golden chainspec hash test pinned in `populate_test.go` — any change to entity generation or JSON emission that alters output will fail CI.
- `prepareDatadir` test with a pre-populated mock datadir — prevents silent stale-state footguns.

---

## 9. Documentation updates

- `client/nethermind/doc.go` — authoritative usage, limitations, engine choice, version pin.
- `README.md` — add `--client=nethermind` section mirroring reth's, document:
  - Supported Nethermind version range: tested against `1.36.2` and `1.37.0` (both use Parity-format chainspec natively). Upper bound left open; revisit when 1.38 / 1.39 ship to confirm the CLI flags and `Init.ExitOnBlockNumber` semantics haven't changed.
  - PATH requirement or `NETHERMIND_BIN` env var.
  - Known limitations (no binary-trie, no target-size, no deep-branch).
  - Command to boot the populated datadir.
- Memory update: add a `project_nethermind_client.md` memory file capturing (a) Parity-format choice rationale, (b) `Init.ExitOnBlockNumber=0` trick, (c) the GethGenesisLoader version gap that drove the decision.

---

## 10. Open questions / to be resolved during implementation

1. **Default `KeepChainSpec` — true or false?** I've proposed true (Nethermind rereads it on every boot). Reth uses false by default. Impact: if false, the operator must pass `--Init.ChainSpecPath` pointing elsewhere on reboot. Recommend true, document clearly.
2. **`engine.NethDev` vs `engine.Ethash` for the default chainspec?** NethDev is the devnet path. Ethash requires `engine.Ethash.params.difficultyBoundDivisor` etc. Simpler to default to NethDev. Confirm it's still supported in 1.37+ (the file `spaceneth.json` ships with 1.37 — yes, supported).
3. **Should `--chain-id` override chainspec's default?** Reth does this via package-level `ChainIDOverride`. Mirror exactly.
4. **Should we validate the post-run datadir against what the user's genesis file declared (e.g., known accounts present)?** Out of scope for v1. Add in a follow-up PR with an RPC-based e2e helper.

---

## Appendix A — CLI flag table (from Agent 1 upstream analysis)

| Flag | Source in Nethermind repo | Effect |
|---|---|---|
| `--config none` | `Nethermind.Runner/configs/none.json` (empty `{}`) | Starts with no preset config; no file required |
| `--Init.ChainSpecPath` | `Nethermind.Api/IInitConfig.cs:30-31` | Path to chainspec JSON |
| `--Init.DiscoveryEnabled false` | `IInitConfig.cs:21-22` (default true) | Disable node discovery |
| `--Init.PeerManagerEnabled false` | `IInitConfig.cs:27-28` (default true) | Disable peer manager |
| `--Init.ProcessingEnabled false` | `IInitConfig.cs:24-25`; `LoadGenesisBlock.cs:27-31` | Stop block processor immediately after genesis |
| `--Init.ExitOnBlockNumber 0` | `IInitConfig.cs:87-88`; `ExitOnBlockNumberHandler.cs:27` | Self-exit with code 0 after genesis added to main chain |
| `--Sync.NetworkingEnabled false` | `ISyncConfig.cs:13-14` | Disable sync networking |
| `--Sync.SynchronizationEnabled false` | `ISyncConfig.cs:16-17` | Disable block download/process |
| `--JsonRpc.Enabled false` | `IJsonRpcConfig.cs:10-13` (default false) | Explicit, defensive |
| `--Merge.Enabled false` | `IMergeConfig.cs` (default TRUE — must disable explicitly) | Disable Engine API and merge plugin |
| `--Network.MaxActivePeers 0` | `INetworkConfig.cs` (default 50) | Cap peers |
| `--Blocks.GenesisTimeoutMs 600000` | `IBlocksConfig.cs` (default 40000) | 10-minute timeout for huge-genesis load (issue #7361) |
| `--log info` | Nethermind global | Ensure "Initialization Completed" banner reaches stderr |

---

## Appendix B — Cited upstream sources

- [NethermindEth/nethermind — master tree](https://github.com/NethermindEth/nethermind)
- [`Nethermind.Db/DbNames.cs`](https://github.com/NethermindEth/nethermind/blob/master/src/Nethermind/Nethermind.Db/DbNames.cs) — 19 DB names
- [`Nethermind.Trie/NodeStorage.cs`](https://github.com/NethermindEth/nethermind/blob/master/src/Nethermind/Nethermind.Trie/NodeStorage.cs) — HalfPath key encoding (informational, for direct-DB alternative)
- [`Nethermind.Specs/ChainSpecStyle/GethGenesisLoader.cs`](https://github.com/NethermindEth/nethermind/blob/master/src/Nethermind/Nethermind.Specs/ChainSpecStyle/GethGenesisLoader.cs) — geth-genesis loader (master-only, not in 1.37)
- [`Nethermind.Specs/ChainSpecStyle/AutoDetectingChainSpecLoader.cs`](https://github.com/NethermindEth/nethermind/blob/master/src/Nethermind/Nethermind.Specs/ChainSpecStyle/AutoDetectingChainSpecLoader.cs) — JSON-shape dispatch
- [`Nethermind.Init/Steps/LoadGenesisBlock.cs`](https://github.com/NethermindEth/nethermind/blob/master/src/Nethermind/Nethermind.Init/Steps/LoadGenesisBlock.cs) — genesis loading step
- [`Nethermind.Init/Steps/InitializeBlockTree.cs`](https://github.com/NethermindEth/nethermind/blob/master/src/Nethermind/Nethermind.Init/Steps/InitializeBlockTree.cs) — ExitOnBlockNumber registration
- [`Nethermind.Blockchain/ExitOnBlockNumberHandler.cs`](https://github.com/NethermindEth/nethermind/blob/master/src/Nethermind/Nethermind.Blockchain/ExitOnBlockNumberHandler.cs) — self-exit logic
- [`Nethermind.Consensus/Processing/GenesisLoader.cs`](https://github.com/NethermindEth/nethermind/blob/master/src/Nethermind/Nethermind.Consensus/Processing/GenesisLoader.cs) — genesis commit
- [`Nethermind.Core/ThisNodeInfo.cs`](https://github.com/NethermindEth/nethermind/blob/master/src/Nethermind/Nethermind.Core/ThisNodeInfo.cs) — "Initialization Completed" banner
- [`Nethermind.Runner/configs/spaceneth.json`](https://github.com/NethermindEth/nethermind/blob/master/src/Nethermind/Nethermind.Runner/configs/spaceneth.json) — NethDev engine template
- [Nethermind Configuration docs](https://docs.nethermind.io/fundamentals/configuration/)
- [PR #6499 — Flat / path-based state layout](https://github.com/NethermindEth/nethermind/pull/6499)
- [PR #10046 — EIP-7949 geth-genesis loader](https://github.com/NethermindEth/nethermind/pull/10046)
- Issue [#7361 — 1GB genesis load timeout](https://github.com/NethermindEth/nethermind/issues/7361)
- Issue [#9066 — SIGTERM hang on multi-CPU Linux](https://github.com/NethermindEth/nethermind/issues/9066)
- Issue [#4523 — invalid chainspec hangs process](https://github.com/NethermindEth/nethermind/issues/4523)
