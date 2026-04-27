# Vendored Nethermind fixtures

Source: `github.com/NethermindEth/nethermind`
Pinned SHA: **`09bd5a2d`** (2026-04-26)
Source path: `src/Nethermind/Nethermind.Blockchain.Test/Specs/`

## Files

| File | Upstream golden hash (genesis block) | Source: GenesisBuilderTests.cs:* |
|---|---|---|
| `empty_accounts_and_storages.json` | `0x61b2253366eab37849d21ac066b96c9de133b8c58a9a38652deae1dd7ec22e7b` | line 25-27 |
| `empty_accounts_and_codes.json` | `0xfa3da895e1c2a4d2673f60dd885b867d60fb6d823abaf1e5276a899d7e2feca5` | line 30-33 |
| `hive_zero_balance_test.json` | `0x62839401df8970ec70785f62e9e9d559b256a9a10b343baf6c064747b094de09` | line 36-39 |

These three hashes are the **B6 differential oracle** for state-actor's
Nethermind writer. When state-actor's pipeline produces these exact bytes
for these exact chainspec inputs, the writer is byte-equivalent to a database
Nethermind itself would have written — by RLP + Keccak determinism, no other
configuration of bytes can yield those hashes.

## Refresh procedure

If the pinned SHA changes:

```sh
for f in empty_accounts_and_storages empty_accounts_and_codes hive_zero_balance_test; do
  git -C /path/to/nethermind show upstream/master:src/Nethermind/Nethermind.Blockchain.Test/Specs/$f.json \
    > internal/neth/testdata/$f.json
done
```

Update the pinned SHA + golden hashes in this file. The B6 e2e CI workflow
runs `cmp` against the upstream files for the pinned SHA and fails if a
manual edit drifts the vendored copies.

## Format note

These chainspecs are **OpenEthereum-format** (engine / params / accounts.builtin
top-level keys), NOT geth-format genesis JSON. B5's `--genesis` flag accepts
geth-format only — B6's oracle test must therefore either parse OpenEthereum
format here OR provide equivalent geth-format JSONs that yield byte-identical
genesis blocks (route TBD when B6 lands).
