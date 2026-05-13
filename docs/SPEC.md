# `--spec` YAML schema

State-actor's `--spec` flag accepts a YAML file declaring concrete entities
(EOAs + contracts) the writer must include in generated genesis state.
Spec entities are written first; the synthetic-fill loop
(`--accounts`/`--contracts`/`--target-size`) runs on top.

## Quick start

```bash
state-actor --client=reth --db=/tmp/mychain --spec=examples/spec-erc20-mixed-sizes.yaml --target-size=20GB
```

## Schema

```yaml
entities:
  - kind: contract | eoa     # required
    name: string             # optional; used for pretty-print + name-derived address
    address: 0x...           # optional; explicit 20-byte address
    balance: "1000000000000000000"  # optional; wei, MUST be a quoted string
    nonce: 0                 # optional; default 0
    code: "0x..."            # optional; required iff template is absent on contracts
    template: erc20          # optional; for kind=contract only
    parameters: { ... }      # optional; template-specific (only with `template`)
    approximate_size_bytes: 1_000_000   # optional; synthesizes storage slots
```

### `kind`

One of:

- `contract` — a smart contract with deployed bytecode. **Must** set
  exactly one of `template:` or `code:`. May set `approximate_size_bytes:`
  to populate storage at scale.
- `eoa` — an externally-owned account. May set `code:` (e.g. an EIP-7702
  23-byte `0xef0100<addr>` delegation marker). May set
  `approximate_size_bytes:` for delegated-storage bloat. MUST NOT set
  `template:` or `parameters:`.

### Address resolution (three deterministic modes)

1. **Explicit**: `address: 0xABC...` is set — used verbatim.
2. **Name-derived**: `address:` omitted, `name:` set —
   `keccak256(seed || name)[12:]`. Same `name + --seed` always produces
   the same address (good for cross-run determinism).
3. **Position-derived**: both omitted —
   `keccak256(seed || "anon-N")[12:]` where `N` is the entity's index.
   Reordering entities in the YAML changes their derived addresses;
   explicit/named entities are stable across reorderings.

### `balance`

Wei, **must be a quoted string**. Unquoted balances are rejected because
YAML's scalar resolution would silently lose precision for values
larger than 2^53. Decimal and `0x`-prefix hex are both accepted:

```yaml
balance: "1000000000000000000"        # 1 ETH decimal
balance: "0xde0b6b3a7640000"          # 1 ETH hex
```

### `approximate_size_bytes`

Target on-disk byte budget for this entity's storage. Resolved to a
synthetic slot count via a per-client calibration factor
(see `internal/sizecal/factors.json`). Slots are populated with
deterministic `(key, value)` pairs derived from `(seed, address)`.

- **v1 limit**: total spec storage materializes into a Go map before
  writers consume it. Practical limit ~1 GB per entity on a 16 GB machine.
  Multi-GB per-entity workloads will gain a streaming writer integration
  in v1.5 (no schema change).
- **Accuracy**: ±25%. The factor is hand-tuned for v1; an empirical
  calibration nightly job will replace it (see plan Task 30).

## Templates

| Template | Required parameters | Optional | Notes |
|---|---|---|---|
| `erc20`  | `symbol`, `name`, `decimals` | `holders` | OpenZeppelin v5 storage layout. `_balances` mapping synthesized per holder. **v1**: stub runtime bytecode — storage is correct but RPC calls return zero. Real OZ v5 bytecode is a one-file v1.5 swap. |

Built-in non-template handlers (no `template:` field needed):

- `raw` — `kind: contract` with explicit `code:`. Whatever bytecode you
  supply, with synthesized storage filling `approximate_size_bytes`.
- `eoa` — `kind: eoa`. Plain EOA when `code:` is empty; 7702-delegating
  EOA when `code:` is `0xef0100<addr>`; storage-bloated EOA when
  `approximate_size_bytes:` is set.

## Composability with existing flags

- `--accounts`, `--contracts`, `--min-slots`, `--max-slots`,
  `--distribution`: still drive the synthetic-fill loop. Spec entities
  are written first, then the loop runs on top.
- `--target-size`: still bounds the synthetic-fill loop. **If spec
  entities alone exceed `--target-size`, `Config.Validate()` fails
  loudly** with copy-pasteable guidance — no silent truncation.
- `--seed`: drives both the spec's deterministic address derivation
  AND the synthetic-fill loop's RNG. Same `--seed + --spec` always
  produces the same on-disk state on a given client.

## Determinism guarantees

Same YAML + same `--seed` produces:
- Identical entity addresses (all three modes). Pinned at unit level by
  `internal/specbuild/derive_test.go:TestResolveAddressDeterministicAcrossRuns`.
- Identical synthesized storage slot keys + values. Pinned by
  `internal/templates/sizing_test.go:TestSynthesizeSlotsDeterministic`.
- Identical end-to-end `PreAlloc` slice after parse → validate → build.
  Pinned by `internal/specbuild/build_test.go:TestBuildDeterminismEndToEnd`.

**v1 CI coverage**: the geth end-to-end suite
(`client/geth/e2e_test.go:TestE2ESuiteSpec`) loads
`examples/spec-ci-min.yaml`, runs Populate, boots geth, runs spamoor,
and goes through the same RPC re-query phases as the synthetic-fill
suite. This pins that `--spec` actually produces bootable, RPC-queryable
state.

**v1.5 follow-up**: cross-client byte-equal state root verification
across geth/besu/nethermind/reth needs Docker-driven boots of the three
cgo clients (besu/nethermind/reth) — those land alongside the
`cross-client-spec-genesis-root` aggregator job in a follow-up PR. To
keep the cross-client invariant robust against per-client storage
calibration drift, that suite passes `sizecal.NewFixed(N)` (a fixed
bytes-per-slot factor) rather than the per-client `sizecal.Default()`.

## Removed flag

- `--inject-accounts` was removed. The equivalent YAML is:

  ```yaml
  entities:
    - kind: eoa
      address: 0xf39Fd6e51aad88F6F4ce6aB8827279cffFb92266
      balance: "999999999000000000000000000"
  ```

## Examples

- `examples/spec-erc20-mixed-sizes.yaml` — Story 1: three ERC-20s of
  different sizes + five 7702 EOAs.
- `examples/spec-eoa-bloat.yaml` — Story 2: three EIP-7702 EOAs with
  bloated storage (2 GB / 5 GB / 10 GB target).
- `examples/spec-7702.yaml` — focused EIP-7702 delegation showcase.
- `examples/spec-ci-baseline.yaml` — canonical CI fixture exercising
  every schema feature. Used by `cross-client-spec-genesis-root`.
