#!/usr/bin/env bash
# spamoor-100-blocks-besu.sh — smoke-test that Besu dev mode mines blocks
# under spamoor erc20_bloater load.
#
# Pre-req: Besu running with --rpc-http-enabled at 127.0.0.1:8545 (the
# `make smoke-besu-spamoor` Makefile target boots it for you), miner
# enabled, and `spamoor` binary on PATH (or pass SPAMOOR=/abs/path).
#
# Steps:
#   1. Confirm RPC reachable.
#   2. Run spamoor erc20_bloater for 100 blocks against the running node.
#   3. Print final block + spamoor exit status.
set -euo pipefail

RPC="${RPC:-http://127.0.0.1:8545}"
SPAMOOR="${SPAMOOR:-spamoor}"
PRIVKEY="${PRIVKEY:-0x0000000000000000000000000000000000000000000000000000000000000001}"
BLOCKS="${BLOCKS:-100}"

rpc() {
  curl --silent --show-error --connect-timeout 2 --max-time 10 -X POST \
    -H "Content-Type: application/json" \
    --data "$1" \
    "$RPC"
}

call() {
  local method="$1"; shift
  local params="$1"
  rpc "{\"jsonrpc\":\"2.0\",\"method\":\"$method\",\"params\":$params,\"id\":1}"
}

echo "=== state-actor → Besu spamoor smoke test ==="
echo "[1/3] Probing chain ..."
chain_id=$(call eth_chainId '[]' | jq -r .result)
echo "  chain id: $chain_id"
block=$(call eth_blockNumber '[]' | jq -r .result)
echo "  start block: $block"

echo "[2/3] Running spamoor erc20_bloater for $BLOCKS blocks ..."
"$SPAMOOR" erc20_bloater \
  --rpchost "$RPC" \
  --privkey "$PRIVKEY" \
  --max-pending 50 \
  --max-wallets 5 \
  --total-blocks "$BLOCKS"
spamoor_rc=$?

echo "[3/3] Final state ..."
final_block=$(call eth_blockNumber '[]' | jq -r .result)
final_num=$((16#${final_block#0x}))
echo "  final block: $final_block ($final_num)"

if [[ $spamoor_rc -eq 0 && $final_num -ge $BLOCKS ]]; then
  echo "PASS: chain progressed to block $final_num under spamoor load"
  exit 0
else
  echo "FAIL: spamoor_rc=$spamoor_rc final_block=$final_num"
  exit 1
fi
