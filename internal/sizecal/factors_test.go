package sizecal

import "testing"

// TestBytesPerSlotPinned guards the calibration value. Bumping the constant
// is a deliberate decision that requires updating the derivation comment in
// factors.go and re-justifying the empirical anchor.
func TestBytesPerSlotPinned(t *testing.T) {
	if got := bytesPerSlot; got != 140 {
		t.Errorf("bytesPerSlot = %d, want 140", got)
	}
}

// TestBytesPerAccountPinned guards the per-account calibration value.
func TestBytesPerAccountPinned(t *testing.T) {
	if got := bytesPerAccount; got != 175 {
		t.Errorf("bytesPerAccount = %d, want 175", got)
	}
}

// TestSlotCountInvariantAcrossClients is the load-bearing guard for the
// cross-client genesis-root invariance gate. Default().SlotsForBytes MUST
// return the same integer for every client name; if a future refactor
// re-introduces per-client branching, this test fails at unit-test level
// instead of letting the divergence reach CI's cross-client gate.
func TestSlotCountInvariantAcrossClients(t *testing.T) {
	const target uint64 = 1 << 30 // 1 GiB
	s := Default()
	want := s.SlotsForBytes("geth", target)
	for _, client := range []string{"geth", "reth", "nethermind", "besu", "unknown"} {
		if got := s.SlotsForBytes(client, target); got != want {
			t.Errorf("SlotsForBytes(%q, 1GiB) = %d, want %d (must be client-invariant)", client, got, want)
		}
	}
}

func TestDefaultSizer(t *testing.T) {
	s := Default()
	got := s.SlotsForBytes("geth", 10_000_000_000)
	want := int(10_000_000_000 / bytesPerSlot)
	if got != want {
		t.Errorf("SlotsForBytes(geth, 10GB) = %d, want %d", got, want)
	}
}

func TestFixedSizer(t *testing.T) {
	s := NewFixed(100)
	if got := s.SlotsForBytes("ignored", 1000); got != 10 {
		t.Errorf("NewFixed(100).SlotsForBytes(_, 1000) = %d, want 10", got)
	}
	// Zero bytes-per-slot → zero slots (avoid division-by-zero).
	zero := NewFixed(0)
	if got := zero.SlotsForBytes("", 1000); got != 0 {
		t.Errorf("NewFixed(0) should return 0, got %d", got)
	}
}

func TestBytesPerSlotAPI(t *testing.T) {
	for _, client := range []string{"geth", "reth", "nethermind", "besu", "anything"} {
		if got := BytesPerSlot(client); got != bytesPerSlot {
			t.Errorf("BytesPerSlot(%q) = %d, want %d", client, got, bytesPerSlot)
		}
	}
}

func TestBytesPerAccountAPI(t *testing.T) {
	for _, client := range []string{"geth", "reth", "nethermind", "besu", "anything"} {
		if got := BytesPerAccount(client); got != bytesPerAccount {
			t.Errorf("BytesPerAccount(%q) = %d, want %d", client, got, bytesPerAccount)
		}
	}
}
