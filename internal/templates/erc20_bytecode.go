package templates

import (
	_ "embed"
	"encoding/hex"
	"fmt"
	"strings"
)

// ERC20RuntimeBytecode is OpenZeppelin Contracts v5.6.1 ERC20 deployed
// runtime bytecode. decimals() returns 18 unconditionally. Regenerate
// via scripts/regen-erc20-bytecode.sh.

//go:embed erc20_oz_v5.hex
var erc20OzV5HexBlob string

// Embedded as text so reviewers can diff the hex on regeneration.
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
