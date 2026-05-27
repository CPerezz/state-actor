package account

import (
	"encoding/binary"
	"fmt"
	"math/bits"

	"github.com/holiman/uint256"
)

// Account is the state-actor-native subset of Erigon's
// `execution/types/accounts.Account` (`account.go:35-42`). We carry
// only the fields EncodeForStorage emits:
//
//   - Nonce
//   - Balance
//   - Incarnation
//   - CodeHash
//
// Erigon's full Account also has `Root` (per-account storage MPT root)
// and `PrevIncarnation`; neither is in the EncodeForStorage output, and
// state-actor's writer pipeline does not need them.
type Account struct {
	Nonce       uint64
	Balance     uint256.Int
	Incarnation uint64
	CodeHash    [32]byte
}

// EmptyCodeHash is keccak256("") — the canonical "no code" sentinel.
// Erigon (and geth) treat an Account with CodeHash == EmptyCodeHash as
// having no contract code; EncodeForStorage omits the codeHash field.
var EmptyCodeHash = [32]byte{
	0xc5, 0xd2, 0x46, 0x01, 0x86, 0xf7, 0x23, 0x3c,
	0x92, 0x7e, 0x7d, 0xb2, 0xdc, 0xc7, 0x03, 0xc0,
	0xe5, 0x00, 0xb6, 0x53, 0xca, 0x82, 0x27, 0x3b,
	0x7b, 0xfa, 0xd8, 0x04, 0x5d, 0x85, 0xa4, 0x70,
}

// IsEmptyCodeHash reports whether a's CodeHash is the empty-bytecode
// sentinel. Mirrors `account.go:583`.
func (a *Account) IsEmptyCodeHash() bool {
	return a.CodeHash == EmptyCodeHash
}

// EncodingLengthForStorage returns the exact byte length EncodeForStorage
// will produce. Mirrors `account.go:59-79`.
func (a *Account) EncodingLengthForStorage() int {
	structLength := 1 // 1 byte for fieldset
	if !a.Balance.IsZero() {
		structLength += a.Balance.ByteLen() + 1
	}
	if a.Nonce > 0 {
		structLength += bitLenToByteLen(bits.Len64(a.Nonce)) + 1
	}
	if !a.IsEmptyCodeHash() {
		structLength += 33 // 32-byte hash + 1-byte length prefix
	}
	if a.Incarnation > 0 {
		structLength += bitLenToByteLen(bits.Len64(a.Incarnation)) + 1
	}
	return structLength
}

// EncodeForStorage encodes a into buffer using the layout described in
// the package doc. Buffer must have len >= EncodingLengthForStorage().
// Mirrors `account.go:87-134`.
//
// Field ordering on the wire: nonce, balance, incarnation, codeHash —
// SAME as Erigon. The fieldSet bitmask byte at offset 0 indicates which
// fields are present (bit 0 = nonce, bit 1 = balance, bit 2 = incarnation,
// bit 3 = codeHash).
//
// Important detail: nonce + incarnation use a custom MSB-first loop
// (not `binary.BigEndian.PutUint64` into a sub-slice), because the
// encoding is variable-length: a 3-byte nonce emits 3 bytes, not 8.
// The loop at line 95-98 / 117-120 of account.go performs the
// equivalent of "shift-right by 8, store low byte, repeat" starting
// from the highest byte position.
func EncodeForStorage(a Account, buffer []byte) {
	fieldSet := 0
	pos := 1

	if a.Nonce > 0 {
		fieldSet = 1
		nonceBytes := bitLenToByteLen(bits.Len64(a.Nonce))
		buffer[pos] = byte(nonceBytes)
		nonce := a.Nonce
		for i := nonceBytes; i > 0; i-- {
			buffer[pos+i] = byte(nonce)
			nonce >>= 8
		}
		pos += nonceBytes + 1
	}

	if !a.Balance.IsZero() {
		fieldSet |= 2
		balanceBytes := a.Balance.ByteLen()
		buffer[pos] = byte(balanceBytes)
		pos++
		a.Balance.WriteToSlice(buffer[pos : pos+balanceBytes])
		pos += balanceBytes
	}

	if a.Incarnation > 0 {
		fieldSet |= 4
		incarnationBytes := bitLenToByteLen(bits.Len64(a.Incarnation))
		buffer[pos] = byte(incarnationBytes)
		incarnation := a.Incarnation
		for i := incarnationBytes; i > 0; i-- {
			buffer[pos+i] = byte(incarnation)
			incarnation >>= 8
		}
		pos += incarnationBytes + 1
	}

	if !a.IsEmptyCodeHash() {
		fieldSet |= 8
		buffer[pos] = 32
		copy(buffer[pos+1:], a.CodeHash[:])
		// Erigon doesn't bump pos after codeHash (line 130 in
		// account.go is commented out: "//pos += 33"); the variable
		// is unused after this point. We follow the same pattern so
		// the byte sequence matches exactly.
	}

	buffer[0] = byte(fieldSet)
}

// AppendForStorage is the slice-returning variant of EncodeForStorage —
// allocates a fresh buffer of the right size and returns it. Convenient
// when the caller doesn't already own a backing buffer.
func AppendForStorage(a Account, buf []byte) []byte {
	n := a.EncodingLengthForStorage()
	start := len(buf)
	buf = append(buf, make([]byte, n)...)
	EncodeForStorage(a, buf[start:])
	return buf
}

// DecodeForStorage parses the Account stored at enc. Mirrors
// `account.go:446-518`. Returns an error if the buffer is malformed.
func DecodeForStorage(enc []byte) (Account, error) {
	a := Account{CodeHash: EmptyCodeHash}
	if len(enc) == 0 {
		return a, nil
	}
	fieldSet := enc[0]
	pos := 1

	if fieldSet&1 > 0 {
		if pos >= len(enc) {
			return a, fmt.Errorf("account: nonce length byte out of range")
		}
		n := int(enc[pos])
		if len(enc) < pos+n+1 {
			return a, fmt.Errorf("account: nonce field truncated (need %d, have %d)", n, len(enc)-pos-1)
		}
		a.Nonce = bytesToUint64(enc[pos+1 : pos+n+1])
		pos += n + 1
	}
	if fieldSet&2 > 0 {
		if pos >= len(enc) {
			return a, fmt.Errorf("account: balance length byte out of range")
		}
		n := int(enc[pos])
		if len(enc) < pos+n+1 {
			return a, fmt.Errorf("account: balance field truncated (need %d, have %d)", n, len(enc)-pos-1)
		}
		a.Balance.SetBytes(enc[pos+1 : pos+n+1])
		pos += n + 1
	}
	if fieldSet&4 > 0 {
		if pos >= len(enc) {
			return a, fmt.Errorf("account: incarnation length byte out of range")
		}
		n := int(enc[pos])
		if len(enc) < pos+n+1 {
			return a, fmt.Errorf("account: incarnation field truncated (need %d, have %d)", n, len(enc)-pos-1)
		}
		a.Incarnation = bytesToUint64(enc[pos+1 : pos+n+1])
		pos += n + 1
	}
	if fieldSet&8 > 0 {
		if pos >= len(enc) {
			return a, fmt.Errorf("account: codeHash length byte out of range")
		}
		n := int(enc[pos])
		if n != 32 {
			return a, fmt.Errorf("account: codeHash length must be 32, got %d", n)
		}
		if len(enc) < pos+33 {
			return a, fmt.Errorf("account: codeHash field truncated")
		}
		copy(a.CodeHash[:], enc[pos+1:pos+33])
	}
	return a, nil
}

// bitLenToByteLen returns ⌈bitLen/8⌉. Mirrors Erigon's `common.BitLenToByteLen`.
func bitLenToByteLen(bitLen int) int {
	return (bitLen + 7) / 8
}

// bytesToUint64 parses up to 8 big-endian bytes into a uint64. Mirrors
// Erigon's `common.BytesToUint64`.
func bytesToUint64(b []byte) uint64 {
	if len(b) == 0 {
		return 0
	}
	if len(b) >= 8 {
		return binary.BigEndian.Uint64(b[len(b)-8:])
	}
	// Pad-left into a [8]byte, then BE-read.
	var tmp [8]byte
	copy(tmp[8-len(b):], b)
	return binary.BigEndian.Uint64(tmp[:])
}
