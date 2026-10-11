package autofill

import (
	mrand "math/rand"
	"testing"

	"github.com/holiman/uint256"
)

func TestParseStorageModel(t *testing.T) {
	for in, want := range map[string]StorageModel{"": StorageNormal, "normal": StorageNormal, "mainnet": StorageMainnet} {
		got, err := ParseStorageModel(in)
		if err != nil || got != want {
			t.Errorf("ParseStorageModel(%q) = %v, %v; want %v", in, got, err, want)
		}
	}
	if _, err := ParseStorageModel("heavy"); err == nil {
		t.Error("ParseStorageModel(heavy) should error")
	}
}

// The account budget alone sets the total account count, so switching the storage model may move
// accounts between EOAs and contracts but never change their sum.
func TestMainnetPlanKeepsAccountTotal(t *testing.T) {
	const budget = 10 << 30
	normal, err := PlanForBudgetProfile(budget, ProfileMainnet, StorageNormal)
	if err != nil {
		t.Fatal(err)
	}
	mainnet, err := PlanForBudgetProfile(budget, ProfileMainnet, StorageMainnet)
	if err != nil {
		t.Fatal(err)
	}
	if a, b := normal.NumEOAs+normal.NumContracts, mainnet.NumEOAs+mainnet.NumContracts; a-b > 1 || b-a > 1 {
		t.Errorf("account total moved: normal %d, mainnet %d", a, b)
	}
	if mainnet.NumContracts <= normal.NumContracts {
		t.Errorf("mainnet has %d contracts, normal %d: small mainnet contracts should need more of them for the same storage budget",
			mainnet.NumContracts, normal.NumContracts)
	}
}

// Draws must reproduce the calibrated shape: over half the contracts hold one slot, about 97% hold
// at most 28, none exceeds the cap; values carry exactly their drawn significant bytes, about half
// of them 20 (addresses).
func TestMainnetDrawsMatchCalibration(t *testing.T) {
	rng := mrand.New(mrand.NewSource(1))
	const n = 200_000
	var one, small int
	for range n {
		s := drawMainnetSlots(rng)
		if s < 1 || s > mainnetMaxSlots {
			t.Fatalf("slot count %d outside [1, %d]", s, mainnetMaxSlots)
		}
		if s == 1 {
			one++
		}
		if s <= 28 {
			small++
		}
	}
	if got := float64(one) / n; got < 0.545 || got > 0.555 {
		t.Errorf("share with one slot = %.4f, want ≈0.5497", got)
	}
	if got := float64(small) / n; got < 0.968 || got > 0.974 {
		t.Errorf("share with ≤28 slots = %.4f, want ≈0.9712", got)
	}

	plan, err := PlanForBudgetProfile(1<<30, ProfileMainnet, StorageMainnet)
	if err != nil {
		t.Fatal(err)
	}
	var vals, twenty int
	for range 20_000 {
		for _, s := range plan.DrawContract(rng).Storage {
			v := new(uint256.Int).SetBytes32(s.Value[:])
			l := (v.BitLen() + 7) / 8
			if l < 1 || l > 32 {
				t.Fatalf("value %x has %d significant bytes", s.Value, l)
			}
			vals++
			if l == 20 {
				twenty++
			}
		}
	}
	if got := float64(twenty) / float64(vals); got < 0.47 || got > 0.53 {
		t.Errorf("share of 20-byte values = %.4f over %d values, want ≈0.498", got, vals)
	}
}
