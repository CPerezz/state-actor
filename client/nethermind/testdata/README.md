# client/nethermind/testdata

Boot/integration test artifacts for `--client=nethermind`. These pair with the
two-image build (state-actor + Nethermind) to validate that a freshly-emitted
RocksDB datadir boots cleanly and serves dev-mode txs.

## Files

| File | Purpose |
|---|---|
| `sa-dev.json` | Nethermind runner config — points at a `BaseDbPath=/data/db` (legacy layout, before the `db/` subdir was dropped) |
| `sa-dev-v2.json` | Nethermind runner config — `BaseDbPath=/data` (current layout) |
| `chainspecs/sa-dev.json` | Parity-format chainspec: chainID 1337, NethDev engine, all EIPs through Shanghai active at genesis |
| `genesis-funded.json` | geth-format `--genesis` for state-actor; pre-funds 3 dev wallets with 1M ETH each so dev mode can sign txs |
| `send-100-txs.sh` | Sends 100 self-transfers from the first dev wallet, polls `eth_blockNumber` until block 100 |
| `validate-big-db.sh` | Boots Nethermind against a state-actor-produced datadir and runs the full validation: genesis-hash check + state-root match + dev-wallet balance + 100-tx round-trip |

## Reproducing the smoke test

```bash
# 1. Build state-actor + chainspec image
docker build -f Dockerfile.nethermind -t state-actor-nethermind:phaseB-v3 .

# 2. Generate a small dev DB (current layout)
mkdir /tmp/sa-neth && docker run --rm \
  -v /tmp/sa-neth:/data \
  -v $PWD/client/nethermind/testdata:/test:ro \
  state-actor-nethermind:phaseB-v3 \
  --client=nethermind --db=/data --accounts=1000 --contracts=100 \
  --genesis=/test/genesis-funded.json --seed=42 --verbose

# 3. Boot Nethermind against the produced datadir
docker run --rm -d \
  --name neth-smoke \
  -v $PWD/client/nethermind/testdata:/test:ro \
  -v /tmp/sa-neth:/data \
  -p 127.0.0.1:8545:8545 \
  nethermind/nethermind:1.37.0 \
  --config /test/sa-dev-v2.json

# 4. Run the smoke test
bash client/nethermind/testdata/validate-big-db.sh /tmp/sa-neth

# 5. Cleanup
docker stop neth-smoke
```
