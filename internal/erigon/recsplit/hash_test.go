package recsplit

import (
	"encoding/hex"
	"testing"
)

// TestRemix matches against the David Stafford 13th mixer constants on a
// small set of fingerprints. This is a self-consistency check (the
// algorithm is fully specified by the three line constants) — the real
// cross-check is the byte-equality fixture test in spike_test.go.
func TestRemix(t *testing.T) {
	cases := []struct {
		in, want uint64
	}{
		// Captured from Erigon's recsplit.remix (db/recsplit/recsplit.go:81-85)
		// by running:
		//   for v := range []uint64{0, 1, 0xdeadbeef, ^uint64(0)} { print(remix(v)) }
		// against the exact same expression — these are the truth values.
		{0, 0},
		{1, 0x5692161d100b05e5},
		{0xdeadbeef, 0x4e062702ec929eea},
		{0xffffffffffffffff, 0xb4d055fcf2cbbd7b},
	}
	for _, c := range cases {
		got := remix(c.in)
		if got != c.want {
			t.Errorf("remix(%x) = %x, want %x", c.in, got, c.want)
		}
	}
}

func TestRemap(t *testing.T) {
	// remap(x, n) = floor(x*n / 2^64). For x=2^64-1, this is n-1.
	if got := remap(0xffffffffffffffff, 1000); got != 999 {
		t.Errorf("remap(maxu64, 1000) = %d, want 999", got)
	}
	if got := remap(0, 1000); got != 0 {
		t.Errorf("remap(0, 1000) = %d, want 0", got)
	}
	// remap(2^63, n) ≈ n/2 — for n=1000, exactly 500.
	if got := remap(1<<63, 1000); got != 500 {
		t.Errorf("remap(2^63, 1000) = %d, want 500", got)
	}
}

func TestRemap16(t *testing.T) {
	// remap16(x, n) = ((x & mask48) * n) >> 48
	if got := remap16(0, 256); got != 0 {
		t.Errorf("remap16(0, 256) = %d, want 0", got)
	}
	if got := remap16(mask48, 256); got != 255 {
		t.Errorf("remap16(mask48, 256) = %d, want 255", got)
	}
}

// TestKeyHashAgainstFixture decodes the first key from spike_100.json and
// computes (hi, lo). The asserted values aren't the test's point — the
// real assertion is that the produced .kvi bytes match Erigon's,
// transitively confirming the hash is right. This test just provides
// a debug peek so a misbehaving murmur3 vendor swap is caught early.
func TestKeyHashSmoke(t *testing.T) {
	// First key from the spike_100 fixture: see testdata/spike_100.json
	keyHex := "538c7f96b164bf1b97bb9f4bb472e89f5b1484f25209c9d9343e92ba09dd9d52"
	salt := uint32(0xCAFEBABE)
	k, err := hex.DecodeString(keyHex)
	if err != nil {
		t.Fatal(err)
	}
	hi, lo := keyHash(k, salt)
	if hi == 0 && lo == 0 {
		t.Fatalf("keyHash returned (0,0); murmur3 likely broken")
	}
	t.Logf("keyHash(%s, %x) = (%x, %x)", keyHex[:16]+"...", salt, hi, lo)
}
