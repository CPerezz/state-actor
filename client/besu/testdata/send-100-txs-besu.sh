#!/usr/bin/env bash
# send-100-txs-besu.sh — smoke-test that Besu dev mode mines blocks.
#
# Pre-req: Besu running with --rpc-http-enabled=true and
#          --rpc-http-api=ETH,NET,WEB3,PERSONAL,ADMIN at 127.0.0.1:8545,
#          plus --miner-enabled --miner-coinbase=0x7e5f4552... so a dev
#          signer is unlocked.
#
# Steps:
#   1. Confirm chain reachable via eth_blockNumber.
#   2. List accounts via eth_accounts (personal_listAccounts may be off).
#   3. Unlock the dev coinbase via personal_unlockAccount (test password).
#   4. Send 100 self-transfers from coinbase to itself with value=1 wei.
#   5. Poll eth_blockNumber until it's >= 100 or 60s elapses.
#   6. Print final block + tx counts.
set -euo pipefail

RPC="${RPC:-http://127.0.0.1:8545}"
COINBASE="${COINBASE:-0x7e5f4552091a69125d5dfcb7b8c2659029395bdf}"

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

echo "=== state-actor → Besu dev-mode smoke test ==="
echo "[1/6] Probing chain..."
chain_id=$(call eth_chainId '[]' | jq -r .result)
echo "  chain id: $chain_id (expected 0x539 = 1337)"
block=$(call eth_blockNumber '[]' | jq -r .result)
echo "  block:    $block (expected 0x0 at boot)"

echo "[2/6] Listing accounts..."
accounts_raw=$(call eth_accounts '[]')
echo "  $accounts_raw"
addr=$(jq -r '.result[0] // empty' <<< "$accounts_raw")
if [[ -z "$addr" || "$addr" == "null" ]]; then
  # Fall back to the configured coinbase if eth_accounts returns nothing.
  echo "  eth_accounts empty; using configured coinbase: $COINBASE"
  addr="$COINBASE"
fi
echo "  using address: $addr"

echo "[3/6] Unlock dev account (best-effort; ignore errors if PERSONAL gated)..."
# personal_unlockAccount returns true on success; if PERSONAL is not in the
# api list this will return null with an error, and eth_sendTransaction will
# fail loudly below — that's acceptable signal.
unlock=$(call personal_unlockAccount "[\"$addr\",\"\",0]" 2>/dev/null || echo '{}')
echo "  unlock result: $unlock"

echo "[4/6] Sending 100 self-transfers via eth_sendTransaction..."
sent=0
failed=0
for i in $(seq 1 100); do
  tx=$(call eth_sendTransaction "[{\"from\":\"$addr\",\"to\":\"$addr\",\"value\":\"0x1\",\"gas\":\"0x5208\"}]")
  result=$(jq -r .result <<< "$tx")
  if [[ "$result" == "null" || -z "$result" ]]; then
    failed=$((failed+1))
    if [[ $failed -le 3 ]]; then
      err=$(jq -r '.error.message // "unknown"' <<< "$tx")
      echo "  tx $i failed: $err"
    fi
  else
    sent=$((sent+1))
  fi
done
echo "  sent=$sent failed=$failed"

echo "[5/6] Waiting up to 60s for chain to reach block 100..."
deadline=$(( $(date +%s) + 60 ))
while [[ $(date +%s) -lt $deadline ]]; do
  block=$(call eth_blockNumber '[]' | jq -r .result)
  num=$((16#${block#0x}))
  if [[ $num -ge 100 ]]; then
    echo "  reached block $num"
    break
  fi
  sleep 1
done

echo "[6/6] Final state:"
final_block=$(call eth_blockNumber '[]' | jq -r .result)
final_num=$((16#${final_block#0x}))
echo "  final block: $final_block ($final_num)"

echo
if [[ $final_num -ge 100 && $sent -ge 95 ]]; then
  echo "PASS: chain progressed to block $final_num with $sent successful txs"
  exit 0
else
  echo "FAIL: final_block=$final_num sent=$sent failed=$failed"
  exit 1
fi
