#!/usr/bin/env bash
# spamoor-100-blocks.sh — runs spamoor's erc20_bloater scenario against
# the local Nethermind RPC and waits until the chain advances 100 blocks
# past genesis. Confirms a real-traffic dev workload mines on top of a
# state-actor-produced datadir.
#
# Pre-req: Nethermind running with --JsonRpc.Enabled=true at 127.0.0.1:8545.
set -euo pipefail

RPC="${RPC:-http://127.0.0.1:8545}"
# spamoor binary path — override via env. Build with `make` in
# https://github.com/ethpandaops/spamoor or pull `ethpandaops/spamoor` Docker.
SPAMOOR="${SPAMOOR:-spamoor}"
# Funded dev wallet from genesis-funded.json (matches NethDev's deterministic
# 0x...01 → 0x7e5f4552... address derivation).
PRIVKEY="${PRIVKEY:-0x0000000000000000000000000000000000000000000000000000000000000001}"
TARGET_BLOCKS="${TARGET_BLOCKS:-100}"
LOG_FILE="${LOG_FILE:-/tmp/spamoor-100b.log}"

if ! command -v "$SPAMOOR" >/dev/null 2>&1 && ! [[ -x "$SPAMOOR" ]]; then
  echo "spamoor: binary not found at '$SPAMOOR'."
  echo "  Set SPAMOOR=/path/to/spamoor or add it to PATH."
  echo "  Build: git clone https://github.com/ethpandaops/spamoor && cd spamoor && make"
  exit 1
fi

rpc_block() {
  curl -s -X POST -H 'Content-Type: application/json' \
    --data '{"jsonrpc":"2.0","method":"eth_blockNumber","params":[],"id":1}' \
    "$RPC" | jq -r .result
}

start_block_hex=$(rpc_block)
start_block=$((16#${start_block_hex#0x}))
target_block=$((start_block + TARGET_BLOCKS))
echo "spamoor: starting at block $start_block, target $target_block"
echo "spamoor: log → $LOG_FILE"

# Run spamoor in background. Use a 250ms slot duration so the rate
# limiter doesn't gate at NethDev's auto-mine cadence; --target-gb is set
# high so the scenario doesn't self-terminate before we've seen 100 blocks.
"$SPAMOOR" erc20_bloater \
  --rpchost="$RPC" \
  --privkey="$PRIVKEY" \
  --seed="state-actor-50g-smoke-$$" \
  --target-gb=10 \
  --target-gas-ratio=0.5 \
  --slot-duration=250ms \
  --wallet-count=10 \
  --refill-amount=10 \
  -v > "$LOG_FILE" 2>&1 &
SPAMOOR_PID=$!
trap 'kill $SPAMOOR_PID 2>/dev/null || true' EXIT

deadline=$(( $(date +%s) + 600 ))
last_print=0
while [[ $(date +%s) -lt $deadline ]]; do
  if ! kill -0 "$SPAMOOR_PID" 2>/dev/null; then
    echo "spamoor: process exited unexpectedly. Last 30 log lines:"
    tail -30 "$LOG_FILE"
    exit 1
  fi
  block_hex=$(rpc_block)
  block=$((16#${block_hex#0x}))

  now=$(date +%s)
  if (( now - last_print >= 5 )); then
    echo "$(date +%H:%M:%S) block=$block (target $target_block, +$((block - start_block)))"
    last_print=$now
  fi

  if (( block >= target_block )); then
    echo "spamoor: reached target block $block (started $start_block, mined $((block - start_block)) blocks)"
    kill "$SPAMOOR_PID" 2>/dev/null || true
    wait "$SPAMOOR_PID" 2>/dev/null || true
    echo "PASS: chain mined $((block - start_block)) blocks under spamoor erc20_bloater load"
    echo "  spamoor log tail:"
    tail -15 "$LOG_FILE" | sed 's/^/    /'
    exit 0
  fi
  sleep 1
done

echo "FAIL: timeout — chain stuck at block $block (target $target_block)"
echo "  spamoor log tail:"
tail -30 "$LOG_FILE" | sed 's/^/    /'
exit 1
