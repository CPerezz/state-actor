# Changelog

## Unreleased

### Added
- **`--spec <file>.yaml` flag** — declarative state customization via YAML.
  Users can specify concrete EOAs + contracts (with optional ERC-20
  template, raw bytecode, EIP-7702 delegation marker, balance/nonce, and
  `approximate_size_bytes` storage bloat). Spec entities are written
  first; the synthetic-fill loop then runs on top until `--target-size`
  is reached. See [`docs/SPEC.md`](docs/SPEC.md) for the schema. Closes
  the customizable-state-generation feature plan.
- Cross-client deterministic invariant: same `--spec` + same `--seed`
  produces identical state root on all four MPT clients
  (geth/besu/nethermind/reth).
- New packages: `internal/spec/` (YAML parser + schema), `internal/templates/`
  (template registry: `erc20`, `raw`, `eoa`), `internal/specbuild/`
  (Spec→entities translator with 3-mode address resolution),
  `internal/sizecal/` (per-client storage-slot calibration table).

### Changed
- `generator.Config` gained a `PreAlloc []templates.PreAllocEntity` field
  populated by `--spec`. `Config.Validate()` materializes PreAlloc into
  the legacy `GenesisAccounts/Code/Storage` maps so existing client
  writer code paths handle spec entities unchanged.
- Nethermind synthetic-accounts writer (`client/nethermind/`) now
  threads alloc storage through the storage-trie path — **closes
  https://github.com/nerolation/state-actor/issues/22**. Specs combining
  storage-bearing entities AND `--accounts`/`--contracts > 0` work on
  nethermind for the first time.

### Removed
- **`--inject-accounts` flag** — superseded by `--spec`. Migration: an
  EOA that was previously written as
  `--inject-accounts=0xABC...` is now declared in YAML as:
  ```yaml
  entities:
    - kind: eoa
      address: 0xABC...
      balance: "999999999000000000000000000"
  ```
  `Config.InjectAddresses` (the programmatic Go field) remains for
  internal test fixtures that wire it directly; only the CLI flag was
  removed.

### Limitations (tracked for v1.5)
- `--spec` materializes `approximate_size_bytes` storage into a Go map
  before writers consume it; per-entity practical limit is ~1 GB on
  16 GB RAM. Multi-GB per-entity workloads (Story 1's "10 GB ERC-20")
  will gain a streaming writer integration in a follow-up that doesn't
  change the schema.
- `erc20` template ships with a stub runtime bytecode in v1 — storage
  layout is correct (OZ v5: `_balances` mapping at slot 0,
  `_totalSupply` at slot 2, short-string `_name`/`_symbol` at slots
  3/4) but `eth_call balanceOf()` returns zero. Audited OZ v5 runtime
  bytecode lands as a one-file v1.5 swap.
- `erc721` and `uniswapv2` templates are deferred to v1.5 (the registry
  pattern makes adding them a single-file change).
