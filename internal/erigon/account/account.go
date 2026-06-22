package account

import (
	"github.com/holiman/uint256"
)

// Account is the state-actor-native subset of Erigon's
// `execution/types/accounts.Account`. We carry only the fields
// SerialiseV3 emits:
//
//   - Nonce
//   - Balance
//   - Incarnation
//   - CodeHash
//
// Erigon's full Account also has `Root` (per-account storage MPT root)
// and `PrevIncarnation`; neither is in the SerialiseV3 output, and
// state-actor's writer pipeline does not need them.
type Account struct {
	Nonce       uint64
	Balance     uint256.Int
	Incarnation uint64
	CodeHash    [32]byte
}

// EmptyCodeHash is keccak256("") — the canonical "no code" sentinel.
// Erigon (and geth) treat an Account with CodeHash == EmptyCodeHash as
// having no contract code; SerialiseV3 emits a zero-length codeHash
// field for it.
var EmptyCodeHash = [32]byte{
	0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c,
	0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0,
	0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b,
	0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70,
}

// IsEmptyCodeHash reports whether a's CodeHash is the empty-bytecode
// sentinel.
func (a *Account) IsEmptyCodeHash() bool {
	return a.CodeHash == EmptyCodeHash
}

// bitLenToByteLen returns ⌈bitLen/8⌉. Mirrors Erigon's
// `common.BitLenToByteLen`.
func bitLenToByteLen(bitLen int) int {
	return (bitLen + 7) / 8
}
