#!/bin/bash
# verify-bloatnet.sh — RPC-verifies a bloatnet state DB against the
# generated spec. Targets the smoke set: spamoor sender, bloated EOAs
# at explicit addresses, ERC-20 showcase, plus N random samples from
# the bulk EOAs and bulk contracts.
#
# Usage:
#   RPC=http://localhost:8545 SAMPLE=10 ./verify-bloatnet.sh
#
# Exits 0 on full pass; first failure aborts with non-zero status.

set -euo pipefail

RPC=${RPC:-http://127.0.0.1:8545}
SAMPLE=${SAMPLE:-500}
BLOCK=${BLOCK:-latest}

# Bulk-EOA layout: indices [0 .. BULK_EOA_PER_HALF-1] are plain,
# [BULK_EOA_PER_HALF .. 2*BULK_EOA_PER_HALF-1] are 7702 delegators.
# Full-scale: 15000 total → 7500 each half. tiny: 50 each.
BULK_EOA_PER_HALF=${BULK_EOA_PER_HALF:-7500}
# Bulk-contracts total: full=200_000, tiny=200.
BULK_CONTRACTS=${BULK_CONTRACTS:-200000}

# Generator-shared constants. KEEP IN SYNC with scripts/gen-bloatnet-spec/main.go.
SPAMOOR_SENDER=0x7e5f4552091a69125d5dfcb7b8c2659029395bdf
SPAMOOR_BAL_WEI=999999999000000000000000000

# Bloated-EOA explicit addresses (from generator's bloatedSpec literals).
BLOAT_100MB=0x00000000000000000000000000000000000b0100
BLOAT_15GB=0x00000000000000000000000000000000000b0f00

# Bloated-EOA name-derived addresses. State-actor's spec-time address
# resolver (internal/specbuild/derive.go:19-34) uses the RUNTIME seed —
# i.e., whatever value run-bloatnet.sh passes as --seed=$SEED, default 42.
# This is DISTINCT from gen-bloatnet-spec's -seed flag (default 4242, an
# RNG seed for slot values only — gen-bloatnet-spec doesn't compute
# entity addresses, it only emits `name:` and lets specbuild derive at
# state-actor time).
#
# The mirror of specbuild.ResolveAddress for name-derived entities is:
#   keccak256( BE_u64(seed) || []byte(name) )[12:]
#
# bloat-5gb is mode=position (index 2 in bloated[]) — addr depends on
# entity-list order; skipping the explicit-check, slot probes only.
SEED=${SEED:-42}                          # matches run-bloatnet.sh:22 + state-actor --seed
SEED_BE_HEX=$(printf '%016x' "$SEED")     # 8-byte BE, lowercase hex, no 0x prefix
bloat_name_addr() {
    local name="$1"
    local addr=$(cast keccak "0x${SEED_BE_HEX}$(printf '%s' "$name" | xxd -p -c0)" | cut -c27-66)
    echo "0x$addr"
}
BLOAT_1GB=$(bloat_name_addr "bloat-1gb-deleg")
BLOAT_30GB=$(bloat_name_addr "bloat-30gb-named")

# Showcase ERC-20 explicit addresses.
BIG_SHOWCASE=0x0000000000000000000000000000000000c0ffee

# Raw contracts.
RAW_FAT=0x000000000000000000000000000000000000ba51

# Canonical system-contract addresses (Cancun/Prague + Beacon-chain Deposit
# Contract). state-actor injects all 5 via oracle.AddCanonicalSystemContracts;
# verify here that the writer actually persisted them.
SYS_BEACON_ROOTS=0x000F3df6D732807Ef1319fB7B8bB8522d0Beac02
SYS_HISTORY_STORAGE=0x0000F90827F1C53a10cb7A02335B175320002935
SYS_WITHDRAWAL_QUEUE=0x00000961Ef480Eb55e80D19ad83579A64c007002
SYS_CONSOLIDATION_QUEUE=0x0000BBdDc7CE488642fb579F8B00f3a590007251
SYS_DEPOSIT_CONTRACT=0x00000000219ab540356cBB839Cbe05303d7705Fa

# Output formatting.
GREEN=$'\033[0;32m'; RED=$'\033[0;31m'; YELLOW=$'\033[0;33m'; RESET=$'\033[0m'
pass=0; fail=0

cast_call() {
    cast call --rpc-url "$RPC" "$@" --block "$BLOCK"
}

# `cast call (string)` returns the value with surrounding double quotes;
# strip them for `check` comparisons against bare-string expectations.
cast_call_string() {
    cast_call "$@" | sed 's/^"//;s/"$//'
}

cast_balance() {
    cast balance --rpc-url "$RPC" "$1" --block "$BLOCK"
}

cast_nonce() {
    cast nonce --rpc-url "$RPC" "$1" --block "$BLOCK"
}

cast_code() {
    cast code --rpc-url "$RPC" "$1" --block "$BLOCK"
}

# Genesis-pinned variants. Use these when the check semantically targets
# the genesis-time state regardless of how far the chain has advanced —
# e.g. the spamoor sender's pre-funded balance and nonce_at_genesis. The
# unpinned helpers above honour $BLOCK (default "latest") and would
# legitimately disagree with the genesis state after spamoor consumes the
# sender's balance + bumps its nonce.
cast_balance_at() {
    cast balance --rpc-url "$RPC" "$1" --block "$2"
}

cast_nonce_at() {
    cast nonce --rpc-url "$RPC" "$1" --block "$2"
}

check() {
    local label="$1" want="$2" got="$3"
    if [ "$want" = "$got" ]; then
        echo "${GREEN}PASS${RESET} $label"
        pass=$((pass + 1))
    else
        echo "${RED}FAIL${RESET} $label"
        echo "       want: $want"
        echo "       got:  $got"
        fail=$((fail + 1))
    fi
}

check_nonzero() {
    local label="$1" got="$2"
    if [ -n "$got" ] && [ "$got" != "0x" ] && [ "$got" != "0" ]; then
        echo "${GREEN}PASS${RESET} $label (non-zero: ${got:0:20}...)"
        pass=$((pass + 1))
    else
        echo "${RED}FAIL${RESET} $label (expected non-zero, got '$got')"
        fail=$((fail + 1))
    fi
}

# ── Phase A: connection sanity ────────────────────────────────────────
echo
echo "=== A. Connection ==="
check "chain_id" "1337" "$(cast chain-id --rpc-url $RPC)"

# ── Phase B: spamoor sender (queried at $BLOCK, with assertions adapted) ──
# Previously pinned to --block 0 to validate the genesis-time balance + nonce.
# That stopped working after we switched state-actor to mark the bench DB as
# "pruned-history" via PruneCheckpoint markers (the MaybeInPlainState fallback
# for AccountsHistory — see plan on-the-meantime-i-proud-karp.md): historical
# queries against any block return StateAtBlockPruned, which is the correct
# semantic for a non-archive node.
#
# Pre-spamoor (chain still at block 0): balance == SPAMOOR_BAL_WEI, nonce == 0.
# Post-spamoor (chain advanced): balance is depleted (< SPAMOOR_BAL_WEI) and
# nonce is bumped (> 0). We don't have a robust per-block-aware check, so:
#   - Always assert non-zero balance + readable nonce (sanity).
#   - When CHECK_CHAIN_ADVANCED is set we also confirm nonce > 0.
echo
echo "=== B. Spamoor sender ($SPAMOOR_SENDER, queried at block=$BLOCK) ==="
check_nonzero "balance_nonzero" "$(cast_balance $SPAMOOR_SENDER)"
if [ "${CHECK_CHAIN_ADVANCED:-}" = "1" ]; then
    sender_nonce=$(cast_nonce $SPAMOOR_SENDER)
    if [ "$sender_nonce" -gt 0 ] 2>/dev/null; then
        echo "${GREEN}PASS${RESET} nonce_bumped (nonce=$sender_nonce > 0)"
        pass=$((pass + 1))
    else
        echo "${RED}FAIL${RESET} nonce_bumped (nonce=$sender_nonce, want > 0 — spamoor didn't run?)"
        fail=$((fail + 1))
    fi
else
    check "nonce_at_genesis" "0" "$(cast_nonce $SPAMOOR_SENDER)"
fi

# ── Phase C: bloated EOAs at explicit + name-derived addresses ───────
echo
echo "=== C. Bloated EOAs (all 5: 100MB, 1GB, 5GB position-derived, 15GB, 30GB) ==="
# 100 MB EOA: balance=100 ETH, no nonce, no code, has storage
check_nonzero "bloat-100mb.balance" "$(cast_balance $BLOAT_100MB)"
check        "bloat-100mb.nonce"    "0"   "$(cast_nonce $BLOAT_100MB)"
check        "bloat-100mb.code"     "0x"  "$(cast_code $BLOAT_100MB)"

# 1 GB EOA: name-derived, balance=200 ETH, 7702 delegation marker 0x11×20.
check_nonzero "bloat-1gb.balance"   "$(cast_balance $BLOAT_1GB)"
EXPECTED_1GB_CODE=0xef01001111111111111111111111111111111111111111
check        "bloat-1gb.code"       "$EXPECTED_1GB_CODE" "$(cast_code $BLOAT_1GB)"

# 15 GB EOA: balance=15 ETH, nonce=7, 7702 delegation marker 0x22×20.
check_nonzero "bloat-15gb.balance"  "$(cast_balance $BLOAT_15GB)"
check        "bloat-15gb.nonce"     "7"   "$(cast_nonce $BLOAT_15GB)"
EXPECTED_15GB_CODE=0xef01002222222222222222222222222222222222222222
check        "bloat-15gb.code"      "$EXPECTED_15GB_CODE" "$(cast_code $BLOAT_15GB)"

# 30 GB EOA: name-derived, balance=30 ETH, nonce=1, no code.
check_nonzero "bloat-30gb.balance"  "$(cast_balance $BLOAT_30GB)"
check        "bloat-30gb.nonce"     "1"   "$(cast_nonce $BLOAT_30GB)"
check        "bloat-30gb.code"      "0x"  "$(cast_code $BLOAT_30GB)"

# 5 GB EOA: position-derived (no name, no explicit addr) — address
# depends on entity-list order in the generated spec. The cross-client
# invariance gate will catch any divergence here at the genesis state
# root, so we skip the per-attribute RPC checks. If we ever need to
# probe specifically: parse the spec YAML to extract index 2's address.

# ── Phase D: ERC-20 showcase (big-showcase at explicit address) ───────
echo
echo "=== D. ERC-20 showcase (big-showcase at $BIG_SHOWCASE) ==="
check "big.name"        "BigShowcase" "$(cast_call_string $BIG_SHOWCASE 'name()(string)')"
check "big.symbol"      "BIG"          "$(cast_call_string $BIG_SHOWCASE 'symbol()(string)')"
check "big.decimals"    "18"           "$(cast_call $BIG_SHOWCASE 'decimals()(uint8)')"
check_nonzero "big.totalSupply"        "$(cast_call $BIG_SHOWCASE 'totalSupply()(uint256)')"

# Big-showcase code is the OZ v5 runtime — non-empty.
check_nonzero "big.code"               "$(cast_code $BIG_SHOWCASE)"

# ── Phase E: raw contract storage ─────────────────────────────────────
echo
echo "=== E. Raw contracts ==="
check_nonzero "raw-fat.code"   "$(cast_code $RAW_FAT)"

# ── Phase F: sample bulk EOAs (name-derived) ──────────────────────────
echo
echo "=== F. Sample bulk EOAs ($SAMPLE samples, BULK_EOA_PER_HALF=$BULK_EOA_PER_HALF) ==="
# Bulk EOA names: "eoa-plain-N" for N in 0..PER_HALF-1 (then "eoa-deleg-N"
# for the second half). Address = keccak256(8B-BE-seed || name)[12:]
# with seed=42 → 8-byte BE = 0x000000000000002a.
PLAIN_HITS=0; PLAIN_MISSES=0
MAX=$((BULK_EOA_PER_HALF - 1))
[ $MAX -lt 0 ] && MAX=0
for i in $(shuf -i 0-$MAX -n $SAMPLE 2>/dev/null || seq 0 $((SAMPLE-1))); do
    [ "$i" -gt "$MAX" ] && continue
    name="eoa-plain-$i"
    addr=$(cast keccak "0x000000000000002a$(printf '%s' "$name" | xxd -p -c0)" | cut -c27-66)
    addr="0x$addr"
    bal=$(cast_balance $addr)
    if [ "$bal" != "0" ]; then
        PLAIN_HITS=$((PLAIN_HITS + 1))
    else
        PLAIN_MISSES=$((PLAIN_MISSES + 1))
    fi
done
if [ "$PLAIN_MISSES" -eq 0 ] && [ "$PLAIN_HITS" -gt 0 ]; then
    echo "${GREEN}PASS${RESET} bulk-plain-EOAs (hits=$PLAIN_HITS, misses=0)"
    pass=$((pass + 1))
else
    echo "${RED}FAIL${RESET} bulk-plain-EOAs (hits=$PLAIN_HITS, misses=$PLAIN_MISSES — require zero misses)"
    fail=$((fail + 1))
fi

# Bulk delegators: name "eoa-deleg-N" for N in [BULK_EOA_PER_HALF, 2*BULK_EOA_PER_HALF-1]
DELEG_HITS=0; DELEG_MISSES=0
DELEG_LO=$BULK_EOA_PER_HALF
DELEG_HI=$((2 * BULK_EOA_PER_HALF - 1))
for i in $(shuf -i $DELEG_LO-$DELEG_HI -n $SAMPLE 2>/dev/null || seq $DELEG_LO $((DELEG_LO+SAMPLE-1))); do
    name="eoa-deleg-$i"
    addr=$(cast keccak "0x000000000000002a$(printf '%s' "$name" | xxd -p -c0)" | cut -c27-66)
    addr="0x$addr"
    bal=$(cast_balance $addr)
    code=$(cast_code $addr)
    # Delegator: balance > 0 AND code is 7702 marker (starts 0xef0100)
    if [ "$bal" != "0" ] && [[ "$code" == 0xef0100* ]]; then
        DELEG_HITS=$((DELEG_HITS + 1))
    else
        DELEG_MISSES=$((DELEG_MISSES + 1))
    fi
done
if [ "$DELEG_MISSES" -eq 0 ] && [ "$DELEG_HITS" -gt 0 ]; then
    echo "${GREEN}PASS${RESET} bulk-delegators (hits=$DELEG_HITS, misses=0)"
    pass=$((pass + 1))
else
    echo "${RED}FAIL${RESET} bulk-delegators (hits=$DELEG_HITS, misses=$DELEG_MISSES — require zero misses)"
    fail=$((fail + 1))
fi

# ── Phase G: sample bulk contracts ────────────────────────────────────
echo
echo "=== G. Sample bulk contracts ($SAMPLE samples, BULK_CONTRACTS=$BULK_CONTRACTS) ==="
RAW_HITS=0
CT_MAX=$((BULK_CONTRACTS - 1))
[ $CT_MAX -lt 0 ] && CT_MAX=0
for i in $(shuf -i 0-$CT_MAX -n $SAMPLE 2>/dev/null || seq 0 2 $((SAMPLE*2))); do
    # Bulk contracts: raw at even indices (ct-raw-N), erc20 at odd (ct-erc20-N)
    if [ $((i % 2)) -eq 0 ]; then
        name="ct-raw-$i"
    else
        name="ct-erc20-$i"
    fi
    addr=$(cast keccak "0x000000000000002a$(printf '%s' "$name" | xxd -p -c0)" | cut -c27-66)
    addr="0x$addr"
    code=$(cast_code $addr)
    if [ -n "$code" ] && [ "$code" != "0x" ]; then
        RAW_HITS=$((RAW_HITS + 1))
    fi
done
if [ "$RAW_HITS" -eq "$SAMPLE" ]; then
    echo "${GREEN}PASS${RESET} bulk-contracts ($RAW_HITS/$SAMPLE — zero misses)"
    pass=$((pass + 1))
else
    echo "${RED}FAIL${RESET} bulk-contracts ($RAW_HITS/$SAMPLE hits — require zero misses; name derivation OR code persistence broken)"
    fail=$((fail + 1))
fi

# ── Phase H: block production ────────────────────────────────────────
echo
echo "=== H. Block production ==="
BN=$(cast block-number --rpc-url $RPC)
echo "       block-number: $BN"
if [ "$BN" -ge 0 ]; then
    echo "${GREEN}PASS${RESET} block-number-readable"
    pass=$((pass + 1))
else
    echo "${RED}FAIL${RESET} block-number-unreadable"
    fail=$((fail + 1))
fi
# Chain-advance check: any post-spamoor call should see BN > 0. Pre-spamoor
# calls (BLOCK=latest at genesis-only DB) tolerate BN=0; gated by the
# caller-set CHECK_CHAIN_ADVANCED=1 env so the genesis-time pre-verify
# doesn't false-FAIL on the empty chain.
if [ "${CHECK_CHAIN_ADVANCED:-}" = "1" ]; then
    if [ "$BN" -gt 0 ]; then
        echo "${GREEN}PASS${RESET} chain-advanced (block=$BN > 0)"
        pass=$((pass + 1))
    else
        echo "${RED}FAIL${RESET} chain-advanced (block=$BN, want > 0 — payload-build or engine-driver stalled)"
        fail=$((fail + 1))
    fi
fi

# ── Phase I: canonical system contracts ──────────────────────────────
# All five mainnet system contracts MUST carry their canonical bytecode
# at genesis. Empty code → either state-actor didn't inject them (bench
# path bug) or the writer dropped them on the floor. Cross-client
# correctness depends on every client persisting all five.
echo
echo "=== I. Canonical system contracts (all 5 must have code) ==="
for sc in "BeaconRoots:$SYS_BEACON_ROOTS" \
          "HistoryStorage:$SYS_HISTORY_STORAGE" \
          "WithdrawalQueue:$SYS_WITHDRAWAL_QUEUE" \
          "ConsolidationQueue:$SYS_CONSOLIDATION_QUEUE" \
          "DepositContract:$SYS_DEPOSIT_CONTRACT"; do
    name=${sc%:*}; addr=${sc##*:}
    code=$(cast_code $addr)
    if [ -n "$code" ] && [ "$code" != "0x" ]; then
        echo "${GREEN}PASS${RESET} sys.$name.code (len=${#code})"
        pass=$((pass + 1))
    else
        echo "${RED}FAIL${RESET} sys.$name.code (empty at $addr — bench-path syscontracts injection broken)"
        fail=$((fail + 1))
    fi
done

# ── Phase J: BEACON_ROOTS ring buffer (post-spamoor only) ────────────
# After at least one block past genesis, EIP-4788's pre-execution call
# should have written timestamp%8191 → block.timestamp at BEACON_ROOTS.
# Gated on CHECK_CHAIN_ADVANCED=1 since pre-spamoor (block=0) has no
# ring-buffer entry to read.
if [ "${CHECK_CHAIN_ADVANCED:-}" = "1" ] && [ "$BN" -gt 0 ]; then
    echo
    echo "=== J. BEACON_ROOTS ring-buffer (post-block-1) ==="
    ts=$(cast block latest --rpc-url $RPC --field timestamp 2>/dev/null || echo 0)
    if [ "$ts" -gt 0 ]; then
        slot=$((ts % 8191))
        val=$(cast storage $SYS_BEACON_ROOTS $slot --rpc-url $RPC 2>/dev/null || echo 0x0)
        if [ "$val" != "0x0000000000000000000000000000000000000000000000000000000000000000" ] && [ "$val" != "0x0" ]; then
            echo "${GREEN}PASS${RESET} beacon-roots-ringbuffer (slot=$slot, val=${val:0:18}…)"
            pass=$((pass + 1))
        else
            echo "${RED}FAIL${RESET} beacon-roots-ringbuffer (slot=$slot empty — EIP-4788 pre-exec didn't write OR sys-contract code was missing)"
            fail=$((fail + 1))
        fi
    fi
fi

# ── summary ──────────────────────────────────────────────────────────
echo
echo "======================================================"
echo "${GREEN}$pass passed${RESET} / ${RED}$fail failed${RESET} (block=$BLOCK, sample=$SAMPLE)"
echo "======================================================"
exit $fail
