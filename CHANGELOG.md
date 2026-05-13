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
- **Deterministic invariant**: same `--spec` + same `--seed` produces
  byte-identical state on the same client. Pinned at unit level
  (address derivation, storage synthesis, end-to-end PreAlloc) and at
  CI level via `client/geth/e2e_test.go:TestE2ESuiteSpec` (geth boot +
  spamoor + golden-hash). Cross-client byte-equal state root across
  geth/besu/nethermind/reth lands in v1.5 (see limitations).
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

### Tested
- Per-package unit tests (`internal/spec/`, `internal/templates/`,
  `internal/specbuild/`, `internal/sizecal/`, `generator/prealloc_test.go`).
- `TestMainSpecFlagSmoke` (in default CI job): builds state-actor, runs
  `--spec` end-to-end against the geth writer, asserts the db dir is
  non-empty. Pins the wiring CLI → parser → templates → specbuild →
  Config.PreAlloc → writer.
- `TestMainInjectAccountsFlagRemoved`: confirms the removed flag exits
  non-zero — prevents an accidental re-add.
- **`client/geth/e2e_test.go:TestE2ESuiteSpec`** (geth-suite CI job):
  loads `examples/spec-ci-min.yaml`, runs Populate, boots geth in --dev
  mode, runs spamoor, captures the genesis state-root, goes through the
  same RunSuitePhases pipeline as the synthetic-fill suite. **This is
  the v1 CI guarantee that `--spec` works end-to-end through writer +
  boot + spamoor.**
- Audit-driven coverage additions:
  - `TestValidateRejectsEIP170OversizeCode` + `TestValidateAcceptsExactlyMaxCodeSize`
  - `TestValidateCaseSensitiveKind` + `TestValidateCaseSensitiveTemplate`
  - `TestParseBalanceRejections` (8 sub-cases: underscored, scientific,
    negative, float, bool, alpha-no-prefix, empty, unquoted-int)
  - `TestParseBalanceMaxUint256` + `TestParseBalanceOverflowUint256`
  - `TestParseAddressEdgeCases` (zero, max, too-long, prefix-only, unquoted-hex)
  - `TestParseCodeEdgeCases` (empty, prefix-only, single-byte, 23-byte 7702 marker, odd-length, non-hex)
  - `TestERC20BalancesSlotComputationManyHolders` (extends single-holder
    Solidity-equivalence to 25 holders)
  - `TestERC20NonceHonorsUserValue` (3 sub-cases pinning the EIP-161 floor + user override)
  - `TestValidateRejectsSpecExceedingTargetSize` + `TestValidateAcceptsSpecUnderTargetSize`
  - `TestBuildDeterminismEndToEnd`

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
- **Cross-client spec-state-root invariant CI is partially landed in v1:
  geth-only (Tier 1).** `client/geth/e2e_test.go:TestE2ESuiteSpec`
  exercises writer → boot → spamoor → golden-hash with `--spec`. The
  besu/nethermind/reth equivalents + a sibling
  `cross-client-spec-genesis-root` aggregator job land in v1.5 — they
  need Docker image builds the v1 PR's author couldn't validate
  locally. Determinism of spec output is pinned at unit level for all
  four clients (the same code path runs identically through every
  `Config.Validate()` shim).
- **ERC-20 template hardcodes nonce-floor at 1.** Per EIP-161, contracts
  on Spurious-Dragon+ forks have nonce ≥ 1. Users who explicitly set
  `nonce: 0` on a `template: erc20` entity get nonce=1 silently.
  Override by setting `nonce: 1` (or higher) explicitly. v1.5 may grow
  a `*uint64` Entity.Nonce to distinguish "unset" from "explicit 0".
