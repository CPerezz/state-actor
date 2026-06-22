package account

import (
	"bytes"
	"testing"

	"github.com/holiman/uint256"
)

// fieldMask is a 4-bit mask: bit 0 = nonce, bit 1 = balance,
// bit 2 = codeHash, bit 3 = incarnation. We cover all 16 combinations
// of "field present / absent" to exhaust SerialiseV3's branching.
//
// Within each present-field, a single non-edge value is sufficient —
// the byte-length-derivation logic (`bitLenToByteLen(bits.Len64(...))`)
// is exercised by the round-trip property: any value that encodes →
// decodes to itself proves the variable-length math is correct.
func TestSerialiseV3_AllFieldsetCombosRoundTrip(t *testing.T) {
	for mask := 0; mask < 16; mask++ {
		mask := mask
		t.Run(testName(mask), func(t *testing.T) {
			a := makeAccount(mask)
			encoded := SerialiseV3(a)
			got, err := DeserialiseV3(encoded)
			if err != nil {
				t.Fatalf("DeserialiseV3(%x): %v", encoded, err)
			}
			if got.Nonce != a.Nonce {
				t.Errorf("Nonce mismatch: got %d want %d", got.Nonce, a.Nonce)
			}
			if !got.Balance.Eq(&a.Balance) {
				t.Errorf("Balance mismatch: got %s want %s", got.Balance.Hex(), a.Balance.Hex())
			}
			if got.CodeHash != a.CodeHash {
				t.Errorf("CodeHash mismatch: got %x want %x", got.CodeHash, a.CodeHash)
			}
			if got.Incarnation != a.Incarnation {
				t.Errorf("Incarnation mismatch: got %d want %d", got.Incarnation, a.Incarnation)
			}
		})
	}
}

// TestSerialiseV3_AllZero verifies the minimum-length 4-byte encoding
// of a fully-zero account. This is a load-bearing edge case: EOAs at
// genesis with zero balance + zero nonce hit this path.
func TestSerialiseV3_AllZero(t *testing.T) {
	a := Account{CodeHash: EmptyCodeHash}
	enc := SerialiseV3(a)
	want := []byte{0x00, 0x00, 0x00, 0x00}
	if !bytes.Equal(enc, want) {
		t.Fatalf("SerialiseV3(zero): got %x, want %x", enc, want)
	}
	dec, err := DeserialiseV3(enc)
	if err != nil {
		t.Fatalf("DeserialiseV3: %v", err)
	}
	if dec.Nonce != 0 || !dec.Balance.IsZero() || dec.CodeHash != EmptyCodeHash || dec.Incarnation != 0 {
		t.Errorf("round-trip drifted: %+v", dec)
	}
}

// TestSerialiseV3_FullAccount covers the maximum-length 84-byte
// encoding (all four fields present with max-width values).
func TestSerialiseV3_FullAccount(t *testing.T) {
	maxBalance := new(uint256.Int)
	maxBalance.SetAllOne()
	a := Account{
		Nonce:       0xFFFFFFFFFFFFFFFF,
		Balance:     *maxBalance,
		Incarnation: 0xFFFFFFFFFFFFFFFF,
		CodeHash:    [32]byte{0xde, 0xad, 0xbe, 0xef, 0xca, 0xfe, 0xba, 0xbe, 0, 0, 0, 0, 0, 0, 0, 0, 0xfa, 0xce, 0xfe, 0xed, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0, 0},
	}
	enc := SerialiseV3(a)
	if len(enc) != 4+8+32+32+8 {
		t.Fatalf("SerialiseV3(max): got len=%d, want 84", len(enc))
	}
	// Field-prefix bytes at known offsets.
	if enc[0] != 8 {
		t.Errorf("nonce-len byte: got %d want 8", enc[0])
	}
	if enc[1+8] != 32 {
		t.Errorf("balance-len byte: got %d want 32", enc[1+8])
	}
	if enc[1+8+1+32] != 32 {
		t.Errorf("codeHash-len byte: got %d want 32", enc[1+8+1+32])
	}
	if enc[1+8+1+32+1+32] != 8 {
		t.Errorf("incarnation-len byte: got %d want 8", enc[1+8+1+32+1+32])
	}
	dec, err := DeserialiseV3(enc)
	if err != nil {
		t.Fatalf("DeserialiseV3: %v", err)
	}
	if dec.Nonce != a.Nonce || !dec.Balance.Eq(&a.Balance) || dec.CodeHash != a.CodeHash || dec.Incarnation != a.Incarnation {
		t.Errorf("round-trip drifted")
	}
}

// TestSerialiseV3_Truncated verifies DeserialiseV3 rejects malformed
// inputs without panicking.
func TestSerialiseV3_Truncated(t *testing.T) {
	cases := [][]byte{
		nil,                       // empty buffer — nonce length byte OOR
		{0x05},                    // nonce-len=5 but no body
		{0x00, 0x20},              // balance-len=32 but no body
		{0x00, 0x00, 0x21},        // codeHash-len=33 (invalid; must be 0 or 32)
		{0x00, 0x00, 0x20},        // codeHash-len=32 but no body
		{0x00, 0x00, 0x00, 0x09},  // incarnation-len=9 (>8)
	}
	for i, c := range cases {
		if _, err := DeserialiseV3(c); err == nil {
			t.Errorf("case %d (%x): expected error, got nil", i, c)
		}
	}
}

func makeAccount(mask int) Account {
	a := Account{CodeHash: EmptyCodeHash}
	if mask&1 != 0 {
		a.Nonce = 0x12345678
	}
	if mask&2 != 0 {
		// A balance that needs > 8 bytes to encode (exercises uint256.WriteToSlice).
		a.Balance = *uint256.NewInt(0).Lsh(uint256.NewInt(1), 200)
	}
	if mask&4 != 0 {
		// Non-empty code hash (any 32 bytes != EmptyCodeHash).
		a.CodeHash = [32]byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12, 13, 14, 15, 16, 17, 18, 19, 20, 21, 22, 23, 24, 25, 26, 27, 28, 29, 30, 31, 32}
	}
	if mask&8 != 0 {
		a.Incarnation = 0x42
	}
	return a
}

func testName(mask int) string {
	const all = "nbci"
	out := make([]byte, 0, 4)
	for i := 0; i < 4; i++ {
		if mask&(1<<i) != 0 {
			out = append(out, all[i])
		} else {
			out = append(out, '-')
		}
	}
	return string(out)
}
