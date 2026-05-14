#!/usr/bin/env bash
# regen-erc20-bytecode.sh — one-shot regenerator for the vendored
# OpenZeppelin v5 ERC20 deployed runtime bytecode used by
# internal/templates/erc20_bytecode.go.
#
# What it does:
#   1. Pulls OZ Solidity sources at the pinned OZ_TAG from GitHub raw.
#   2. Wraps the abstract ERC20 in a no-state concrete subclass.
#   3. Compiles with solc at the pinned settings.
#   4. Writes the runtime bytecode hex (wrapped at 80 cols) to
#      internal/templates/erc20_oz_v5.hex.
#
# Requirements:
#   - solc >= 0.8.20 on PATH (any version satisfies the OZ pragma; 0.8.30
#     was used for the initial vendoring)
#   - curl
#   - python3 (for combined-json extraction + line wrapping)
#
# CI does NOT run this script. The .hex file is trusted; the unit test
# TestERC20RuntimeBytecodePinned in internal/templates/erc20_test.go
# pins the keccak256 of the resulting bytecode. After re-running this
# script, update the pinned hash in that test.

set -euo pipefail

OZ_TAG="${OZ_TAG:-v5.6.1}"
OPTIMIZER_RUNS="${OPTIMIZER_RUNS:-200}"

REPO_ROOT="$(git rev-parse --show-toplevel)"
OUT_HEX="${OUT_HEX:-${REPO_ROOT}/internal/templates/erc20_oz_v5.hex}"

if ! command -v solc >/dev/null 2>&1; then
  echo "error: solc not found on PATH" >&2
  exit 1
fi

SOLC_VERSION="$(solc --version | grep -oE '0\.[0-9]+\.[0-9]+' | head -1)"
echo "OZ_TAG=${OZ_TAG}  solc=${SOLC_VERSION}  optimizer-runs=${OPTIMIZER_RUNS}"

WORK="$(mktemp -d)"
trap 'rm -rf "$WORK"' EXIT

mkdir -p \
  "$WORK/contracts/token/ERC20/extensions" \
  "$WORK/contracts/utils" \
  "$WORK/contracts/interfaces" \
  "$WORK/wrapper"

BASE="https://raw.githubusercontent.com/OpenZeppelin/openzeppelin-contracts/${OZ_TAG}"
echo "Fetching OZ sources from ${BASE} ..."
curl -fsSL "${BASE}/contracts/token/ERC20/ERC20.sol"                            -o "$WORK/contracts/token/ERC20/ERC20.sol"
curl -fsSL "${BASE}/contracts/token/ERC20/IERC20.sol"                           -o "$WORK/contracts/token/ERC20/IERC20.sol"
curl -fsSL "${BASE}/contracts/token/ERC20/extensions/IERC20Metadata.sol"        -o "$WORK/contracts/token/ERC20/extensions/IERC20Metadata.sol"
curl -fsSL "${BASE}/contracts/utils/Context.sol"                                -o "$WORK/contracts/utils/Context.sol"
curl -fsSL "${BASE}/contracts/interfaces/draft-IERC6093.sol"                    -o "$WORK/contracts/interfaces/draft-IERC6093.sol"

# Concrete wrapper around abstract OZ ERC20. Adds no state, no methods —
# its runtime IS the OZ ERC20 dispatcher.
cat > "$WORK/wrapper/Token.sol" <<'SOL'
// SPDX-License-Identifier: MIT
pragma solidity ^0.8.20;

import {ERC20} from "../contracts/token/ERC20/ERC20.sol";

contract Token is ERC20 {
    constructor() ERC20("", "") {}
}
SOL

echo "Compiling Token wrapper with solc ..."
cd "$WORK"
solc --optimize --optimize-runs "${OPTIMIZER_RUNS}" --metadata-hash none \
     --combined-json bin-runtime wrapper/Token.sol \
  | python3 -c '
import sys, json
data = json.load(sys.stdin)
key = next(k for k in data["contracts"] if k.endswith(":Token"))
b = data["contracts"][key]["bin-runtime"]
# Wrap at 80 cols for reviewable diffs.
lines = [b[i:i+80] for i in range(0, len(b), 80)]
sys.stdout.write("\n".join(lines) + "\n")
' > "$OUT_HEX"

BYTES="$(($(wc -c < "$OUT_HEX") - $(wc -l < "$OUT_HEX")))"
# Approximate byte count: hex chars / 2 (subtract newlines first).
BYTES=$(((BYTES) / 2))
echo "Wrote: ${OUT_HEX} (${BYTES} bytes)"
echo
echo "Next steps:"
echo "  1. Recompute the pinned keccak256:"
echo "       cd \"\${REPO_ROOT}\" && go test -run TestERC20RuntimeBytecodePinned ./internal/templates"
echo "  2. If it fails, copy the 'got' hash from the failure into"
echo "     internal/templates/erc20_test.go (TestERC20RuntimeBytecodePinned)."
echo "  3. Update the provenance comment in internal/templates/erc20_bytecode.go"
echo "     if OZ_TAG or solc version changed."
