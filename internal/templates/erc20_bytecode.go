package templates

import (
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
)

// ERC20RuntimeBytecode is the deployed runtime bytecode of OpenZeppelin
// Contracts v5.6.1 ERC20.
//
// Provenance:
//   - Source repository: github.com/OpenZeppelin/openzeppelin-contracts
//   - Pinned tag: v5.6.1
//   - Compiled files: contracts/token/ERC20/ERC20.sol plus its transitive
//     imports IERC20.sol, extensions/IERC20Metadata.sol, utils/Context.sol,
//     and interfaces/draft-IERC6093.sol (for IERC20Errors).
//   - Concrete subclass:
//
//       contract Token is ERC20 { constructor() ERC20("", "") {} }
//
//     OZ v5 marks ERC20 as `abstract contract` to require a derived
//     supply mechanism. The wrapper adds no state and no methods, so
//     its runtime bytecode is the audited OZ ERC20 dispatcher.
//   - Compiler: solc 0.8.30
//   - Settings: --optimize --optimize-runs 200 --metadata-hash none
//
// `decimals()` returns 18 unconditionally (OZ v5's base ERC20 default).
// The `erc20` template's ValidateParameters rejects `decimals != 18`.
//
// To regenerate from upstream OZ + re-pin: scripts/regen-erc20-bytecode.sh

//go:embed erc20_oz_v5.hex
var erc20OzV5HexBlob string

// ERC20RuntimeBytecode is read at package init from the embedded hex
// blob above. Embedded as text (not as raw bytes) so reviewers can read
// the hex diff line-by-line on regeneration.
var ERC20RuntimeBytecode = decodeERC20Bytecode(erc20OzV5HexBlob)

func decodeERC20Bytecode(s string) []byte {
	s = strings.Map(func(r rune) rune {
		switch r {
		case ' ', '\t', '\n', '\r':
			return -1
		}
		return r
	}, s)
	b, err := hex.DecodeString(s)
	if err != nil {
		panic(fmt.Sprintf("erc20_bytecode: decode embedded hex: %v", err))
	}
	return b
}
