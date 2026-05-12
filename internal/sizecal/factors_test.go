package sizecal

import "testing"

func TestFactorsTablePopulated(t *testing.T) {
	// All four clients must have a calibrated factor — a missing entry
	// would silently fall back to the conservative default, which can mask
	// real calibration drift.
	for _, client := range []string{"geth", "besu", "nethermind", "reth"} {
		if v := BytesPerSlot(client); v == 0 || v == fallbackBytesPerSlot {
			t.Errorf("client %q: factor = %d (zero or fallback) — missing or broken in factors.json", client, v)
		}
	}
}

func TestFactorsReasonableRange(t *testing.T) {
	// Sanity bounds: storage slots can't be cheaper than ~32 bytes (one
	// value hash) nor heavier than ~500 bytes (way past observed MPT
	// overhead). Anything outside this range is almost certainly a typo
	// in factors.json.
	const minReasonable = uint64(32)
	const maxReasonable = uint64(500)
	for _, client := range []string{"geth", "besu", "nethermind", "reth"} {
		v := BytesPerSlot(client)
		if v < minReasonable || v > maxReasonable {
			t.Errorf("client %q: factor %d outside reasonable range [%d, %d]", client, v, minReasonable, maxReasonable)
		}
	}
}

func TestDefaultSizer(t *testing.T) {
	s := Default()
	// 10 GB / 64 bytes ≈ 156M slots for geth.
	got := s.SlotsForBytes("geth", 10_000_000_000)
	want := int(10_000_000_000 / BytesPerSlot("geth"))
	if got != want {
		t.Errorf("SlotsForBytes(geth, 10GB) = %d, want %d", got, want)
	}
}

func TestUnknownClientFallback(t *testing.T) {
	s := Default()
	got := s.SlotsForBytes("aleth-fake", 1_000_000)
	want := int(1_000_000 / fallbackBytesPerSlot)
	if got != want {
		t.Errorf("unknown client should use fallback: got %d, want %d", got, want)
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
