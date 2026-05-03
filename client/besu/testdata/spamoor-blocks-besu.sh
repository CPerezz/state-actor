#!/usr/bin/env bash
# spamoor-blocks-besu.sh — run spamoor erc20_bloater against a running Besu
# until the chain advances by $BLOCKS blocks beyond the start.
#
# spamoor's erc20_bloater scenario does NOT have a --total-blocks flag — it
# runs to a target storage size (--target-gb). To get "N blocks of load" we
# launch spamoor in the background, poll eth_blockNumber, and SIGTERM it
# once start+BLOCKS is reached.
#
# Pre-req: $SPAMOOR points at a built spamoor binary, Besu's RPC is at
# http://127.0.0.1:8545, miner enabled with 0x7e5f4552... unlocked.
#
# Args via env:
#   BLOCKS    — target block delta from start (default 200)
#   RPC       — JSON-RPC URL (default http://127.0.0.1:8545)
#   SPAMOOR   — path to spamoor binary (default /Users/random_anon/dev/spamoor/bin/spamoor)
#   PRIVKEY   — funded sender (default genesis-funded.json's 0x7e5f4552 key)
#   TARGET_GB — spamoor target storage size, large enough that we hit BLOCKS
#               first (default 100)
set -euo pipefail

BLOCKS="${BLOCKS:-200}"
RPC="${RPC:-http://127.0.0.1:8545}"
SPAMOOR="${SPAMOOR:-/Users/random_anon/dev/spamoor/bin/spamoor}"
PRIVKEY="${PRIVKEY:-0x0000000000000000000000000000000000000000000000000000000000000001}"
TARGET_GB="${TARGET_GB:-100}"
SPAMOOR_LOG="${SPAMOOR_LOG:-/tmp/spamoor-besu.log}"

call() {
  local method="$1"; shift
  local params="$1"
  curl -s --connect-timeout 2 --max-time 10 -X POST -H 'Content-Type: application/json' \
    --data "{\"jsonrpc\":\"2.0\",\"method\":\"$method\",\"params\":$params,\"id\":1}" "$RPC"
}

block_num() {
  local hex
  hex=$(call eth_blockNumber '[]' | jq -r .result)
  echo $((16#${hex#0x}))
}

echo "=== state-actor → Besu spamoor smoke ==="
chain=$(call eth_chainId '[]' | jq -r .result)
start=$(block_num)
target=$((start + BLOCKS))
echo "  RPC chainId=$chain start_block=$start target_block=$target ($BLOCKS-block delta)"
echo "  spamoor binary=$SPAMOOR  log=$SPAMOOR_LOG  target_gb=$TARGET_GB"

if [[ ! -x "$SPAMOOR" ]]; then
  echo "FAIL: spamoor binary not executable at $SPAMOOR"
  exit 1
fi

echo "[1/2] Starting spamoor erc20_bloater in background ..."
"$SPAMOOR" erc20_bloater \
  --rpchost "$RPC" \
  --privkey "$PRIVKEY" \
  --wallet-count 5 \
  --target-gb "$TARGET_GB" \
  --target-gas-ratio 0.5 \
  > "$SPAMOOR_LOG" 2>&1 &
SPAMOOR_PID=$!
echo "  spamoor pid=$SPAMOOR_PID"

cleanup() {
  if kill -0 "$SPAMOOR_PID" 2>/dev/null; then
    kill -TERM "$SPAMOOR_PID" 2>/dev/null || true
    sleep 1
    kill -KILL "$SPAMOOR_PID" 2>/dev/null || true
  fi
}
trap cleanup EXIT

echo "[2/2] Polling for $BLOCKS-block advance (deadline 30 minutes) ..."
deadline=$(( $(date +%s) + 1800 ))
last_logged=$start
while [[ $(date +%s) -lt $deadline ]]; do
  if ! kill -0 "$SPAMOOR_PID" 2>/dev/null; then
    echo "  spamoor exited unexpectedly. Tail of log:"
    tail -30 "$SPAMOOR_LOG"
    exit 1
  fi
  cur=$(block_num)
  if (( cur >= target )); then
    echo "  reached block $cur (delta=$((cur - start))). PASS."
    exit 0
  fi
  if (( cur - last_logged >= 10 )); then
    echo "  block=$cur delta=$((cur - start))/$BLOCKS"
    last_logged=$cur
  fi
  sleep 2
done

echo "  TIMEOUT: did not reach +$BLOCKS blocks within 30 minutes"
echo "  Tail of spamoor log:"
tail -30 "$SPAMOOR_LOG"
exit 1
