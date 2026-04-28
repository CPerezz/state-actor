#!/usr/bin/env bash
# send-100-txs.sh — smoke-test that Nethermind dev mode mines blocks.
#
# Pre-req: Nethermind running with --JsonRpc.Enabled=true at 127.0.0.1:8545.
#
# Steps:
#   1. Confirm chain reachable via eth_blockNumber.
#   2. List dev-mode accounts (personal_listAccounts).
#   3. Send 100 self-transfers from accounts[0] to itself with value=1 wei.
#   4. Poll eth_blockNumber until it's >= 100 or 60s elapses.
#   5. Print final block + tx counts.
set -euo pipefail

RPC="${RPC:-http://127.0.0.1:8545}"

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

echo "=== state-actor → Nethermind dev-mode smoke test ==="
echo "[1/5] Probing chain..."
chain_id=$(call eth_chainId '[]' | jq -r .result)
echo "  chain id: $chain_id"
block=$(call eth_blockNumber '[]' | jq -r .result)
echo "  block:    $block (expected 0x0 at boot)"

echo "[2/5] Listing accounts..."
accounts_raw=$(call personal_listAccounts '[]')
echo "  $accounts_raw"
addr=$(jq -r '.result[0]' <<< "$accounts_raw")
if [[ "$addr" == "null" || -z "$addr" ]]; then
  # Fall back to eth_accounts if personal_ namespace is gated.
  accounts_raw=$(call eth_accounts '[]')
  addr=$(jq -r '.result[0]' <<< "$accounts_raw")
fi
echo "  using address: $addr"

if [[ "$addr" == "null" || -z "$addr" ]]; then
  echo "FAIL: no dev account available"
  exit 1
fi

echo "[3/5] Funding self with eth_sendTransaction loop..."
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

echo "[4/5] Waiting up to 60s for chain to reach block 100..."
deadline=$(( $(date +%s) + 60 ))
while [[ $(date +%s) -lt $deadline ]]; do
  block=$(call eth_blockNumber '[]' | jq -r .result)
  echo "  block: $block"
  num=$((16#${block#0x}))
  if [[ $num -ge 100 ]]; then
    echo "  reached block $num"
    break
  fi
  sleep 1
done

echo "[5/5] Final state:"
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
