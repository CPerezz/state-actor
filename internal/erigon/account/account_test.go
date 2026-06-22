package account

import (
	"bytes"
	"encoding/hex"
	"encoding/json"
	"math/big"
	"os"
	"path/filepath"
	"testing"

	"github.com/holiman/uint256"
)

// fixture mirrors the JSON schema from
// upstream Erigon's reference account encoder.
type fixture struct {
	Label       string `json:"label"`
	Nonce       uint64 `json:"nonce"`
	BalanceHex  string `json:"balance_hex"`
	Incarnation uint64 `json:"incarnation"`
	CodeHashHex string `json:"code_hash_hex"`
	ExpectedHex string `json:"expected_hex"`
}

// TestEncodeForStorageAgainstErigon is the byte-equality test: for every
// fixture captured by running Erigon's reference Account.EncodeForStorage
// (see upstream Erigon's reference account encoder), our pure-Go
// EncodeForStorage must produce byte-identical output.
//
// This is the cross-verify check Architect B's "own the format" design
// hinges on for the accounts domain — if any byte diverges, our writer
// will produce Erigon-unreadable Tbl*Vals records / .kv snapshot entries.
//
// The golden fixtures are committed under testdata/; they were captured
// from upstream Erigon v3.4.2's reference Account.EncodeForStorage.
func TestEncodeForStorageAgainstErigon(t *testing.T) {
	path := filepath.Join("testdata", "erigon_golden.json")
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v (golden is committed under testdata/)", path, err)
	}
	var fixtures []fixture
	if err := json.Unmarshal(data, &fixtures); err != nil {
		t.Fatalf("parse %s: %v", path, err)
	}
	if len(fixtures) == 0 {
		t.Fatal("no fixtures in golden file")
	}

	for _, f := range fixtures {
		f := f
		t.Run(f.Label, func(t *testing.T) {
			balance := new(big.Int)
			if f.BalanceHex != "" {
				b, err := hex.DecodeString(f.BalanceHex)
				if err != nil {
					t.Fatalf("decode balance hex: %v", err)
				}
				balance.SetBytes(b)
			}
			chBytes, err := hex.DecodeString(f.CodeHashHex)
			if err != nil {
				t.Fatalf("decode code_hash hex: %v", err)
			}
			if len(chBytes) != 32 {
				t.Fatalf("code_hash must be 32 bytes, got %d", len(chBytes))
			}
			var codeHash [32]byte
			copy(codeHash[:], chBytes)

			balU256, overflow := uint256.FromBig(balance)
			if overflow {
				t.Fatalf("balance %s overflows uint256", balance.String())
			}
			a := Account{
				Nonce:       f.Nonce,
				Balance:     *balU256,
				Incarnation: f.Incarnation,
				CodeHash:    codeHash,
			}

			want, err2 := hex.DecodeString(f.ExpectedHex)
			if err2 != nil {
				t.Fatalf("decode expected hex: %v", err2)
			}

			gotLen := a.EncodingLengthForStorage()
			if gotLen != len(want) {
				t.Errorf("EncodingLengthForStorage mismatch: got=%d want=%d", gotLen, len(want))
			}
			got := AppendForStorage(a, nil)
			if !bytes.Equal(got, want) {
				firstDiff := -1
				n := len(got)
				if len(want) < n {
					n = len(want)
				}
				for i := 0; i < n; i++ {
					if got[i] != want[i] {
						firstDiff = i
						break
					}
				}
				t.Fatalf("byte mismatch for %s: first differs at byte %d\n  got:  %x\n  want: %x\n  lengths: got=%d want=%d",
					f.Label, firstDiff, got, want, len(got), len(want))
			}

			// Round-trip: decode our bytes; must round-trip equal.
			decoded, err := DecodeForStorage(got)
			if err != nil {
				t.Fatalf("DecodeForStorage round-trip: %v", err)
			}
			if decoded.Nonce != a.Nonce {
				t.Errorf("round-trip nonce: got=%d want=%d", decoded.Nonce, a.Nonce)
			}
			if decoded.Balance.Cmp(&a.Balance) != 0 {
				t.Errorf("round-trip balance: got=%s want=%s", decoded.Balance.String(), a.Balance.String())
			}
			if decoded.Incarnation != a.Incarnation {
				t.Errorf("round-trip incarnation: got=%d want=%d", decoded.Incarnation, a.Incarnation)
			}
			if decoded.CodeHash != a.CodeHash {
				t.Errorf("round-trip codeHash: got=%x want=%x", decoded.CodeHash, a.CodeHash)
			}
		})
	}
}

// TestEmptyAccountSingleByte: a fresh empty account encodes to exactly
// one byte (the fieldSet=0 sentinel).
func TestEmptyAccountSingleByte(t *testing.T) {
	a := Account{CodeHash: EmptyCodeHash}
	if got := a.EncodingLengthForStorage(); got != 1 {
		t.Errorf("empty account length: got=%d want=1", got)
	}
	got := AppendForStorage(a, nil)
	if len(got) != 1 || got[0] != 0 {
		t.Errorf("empty account bytes: got=%x want=[00]", got)
	}
}

// TestIsEmptyCodeHash sanity-checks the sentinel comparison.
func TestIsEmptyCodeHash(t *testing.T) {
	a := Account{CodeHash: EmptyCodeHash}
	if !a.IsEmptyCodeHash() {
		t.Error("EmptyCodeHash should compare equal to itself")
	}
	a.CodeHash[0] ^= 1
	if a.IsEmptyCodeHash() {
		t.Error("perturbed CodeHash should NOT compare equal to EmptyCodeHash")
	}
}
