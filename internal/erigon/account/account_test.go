package account

import "testing"

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
