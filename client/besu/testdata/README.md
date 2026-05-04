# client/besu/testdata

Test fixtures and smoke-test scripts for the Besu client adapter.

## Vendored Besu fixtures

`genesis1.json` and `genesisNonce.json` are vendored from
hyperledger/besu tag 26.5.0 — see `LICENSE-besu-fixtures` for attribution.
They anchor the Tier 2 differential oracle (`oracle_test.go`):

- `genesis1.json` (2 EOAs, chainId=15) — expected stateRoot
  `0x92683e6af0f8a932e5fe08c870f2ae9d287e39d4518ec544b0be451f1035fd39`
  per `GenesisStateTest.java:74-75`.
- `genesisNonce.json` (1 EOA + 1 contract with code, chainId=1) — expected
  genesis block hash
  `0x36750291f1a8429aeb553a790dc2d149d04dbba0ca4cfc7fd5eb12d478117c9f`
  per `GenesisStateTest.java:157-159`.

## Smoke fixtures

`genesis-funded.json` is a state-actor-authored dev genesis. chainId=1337,
londonBlock=99999999 (avoids baseFee plumbing in the genesis header — v1
supports through Shanghai), 3 funded addresses with 1M ETH each. Used by
`make smoke-besu` and `make smoke-besu-spamoor`.

## Smoke scripts

- `validate-big-db-besu.sh` — boot `hyperledger/besu:25.11.0` against a
  state-actor-produced datadir, verify chainId, block 0, balance, then
  send 100 self-transfers via `send-100-txs-besu.sh`.
- `send-100-txs-besu.sh` — send 100 self-transfers via personal_unlock +
  eth_sendTransaction. Used by validate-big-db-besu.sh.
- `spamoor-100-blocks-besu.sh` — run spamoor erc20_bloater for 100 blocks
  against a running Besu, used by `make smoke-besu-spamoor`.

## Reproducer

```
make docker-besu          # build the Besu-capable image (cgo_besu + RocksDB 10.6.2)
make smoke-besu           # generate DB + boot Besu + send 100 txs
make smoke-besu-spamoor   # generate DB + boot Besu + spamoor 100 blocks
make test-besu-oracle     # run TestDifferentialOracle inside the builder image
```
