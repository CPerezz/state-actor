package keys

import (
	"bytes"
	"encoding/hex"
	"testing"

	"github.com/ethereum/go-ethereum/common"
)

// genesisHash is a canonical test fixture (all-zeros with 0xAB at byte 31).
var testGenesisHash = common.HexToHash(
	"0x00000000000000000000000000000000000000000000000000000000000000ab",
)

// TestBlockHeaderKey pins the output for prefix 0x02 ++ 32B hash.
// Source: KeyValueStoragePrefixedKeyBlockchainStorage.java:64.
func TestBlockHeaderKey(t *testing.T) {
	got := BlockHeaderKey(testGenesisHash)
	want, _ := hex.DecodeString(
		"02" + "00000000000000000000000000000000000000000000000000000000000000ab",
	)
	if !bytes.Equal(got, want) {
		t.Fatalf("BlockHeaderKey:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestBlockBodyKey pins prefix 0x03 ++ 32B hash.
// Source: KeyValueStoragePrefixedKeyBlockchainStorage.java:65.
func TestBlockBodyKey(t *testing.T) {
	got := BlockBodyKey(testGenesisHash)
	want, _ := hex.DecodeString(
		"03" + "00000000000000000000000000000000000000000000000000000000000000ab",
	)
	if !bytes.Equal(got, want) {
		t.Fatalf("BlockBodyKey:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestBlockReceiptsKey pins prefix 0x04 ++ 32B hash.
// Source: KeyValueStoragePrefixedKeyBlockchainStorage.java:66.
func TestBlockReceiptsKey(t *testing.T) {
	got := BlockReceiptsKey(testGenesisHash)
	want, _ := hex.DecodeString(
		"04" + "00000000000000000000000000000000000000000000000000000000000000ab",
	)
	if !bytes.Equal(got, want) {
		t.Fatalf("BlockReceiptsKey:\n  got:  %x\n  want: %x", got, want)
	}
}

// TestCanonicalHashKey_BlockZero pins the genesis canonical hash key:
// prefix 0x05 ++ UInt256(0) = 0x05 ++ 32 zero bytes.
// Source: KeyValueStoragePrefixedKeyBlockchainStorage.java:67.
func TestCanonicalHashKey_BlockZero(t *testing.T) {
	got := CanonicalHashKey(0)
	if len(got) != 33 {
		t.Fatalf("CanonicalHashKey(0) length: got %d, want 33", len(got))
	}
	if got[0] != 0x05 {
		t.Fatalf("CanonicalHashKey(0) prefix: got %#x, want 0x05", got[0])
	}
	for i, b := range got[1:] {
		if b != 0x00 {
			t.Fatalf("CanonicalHashKey(0) byte[%d]=%x, want 0x00", i+1, b)
		}
	}
}

// TestCanonicalHashKey_Block256 tests a non-trivial block number (256 = 0x0100).
// UInt256(256) = 31 zero bytes ++ [0x01, 0x00].
func TestCanonicalHashKey_Block256(t *testing.T) {
	got := CanonicalHashKey(256)
	want, _ := hex.DecodeString(
		"05" +
			"0000000000000000000000000000000000000000000000000000000000000100",
	)
	if !bytes.Equal(got, want) {
		t.Fatalf("CanonicalHashKey(256):\n  got:  %x\n  want: %x", got, want)
	}
}

// TestCanonicalHashKey_MaxUint64 tests the largest representable block number.
// uint64 max = 0xFFFFFFFFFFFFFFFF (8 bytes), padded to 32 bytes with leading zeros.
func TestCanonicalHashKey_MaxUint64(t *testing.T) {
	got := CanonicalHashKey(^uint64(0))
	want, _ := hex.DecodeString(
		"05" +
			"000000000000000000000000000000000000000000000000ffffffffffffffff",
	)
	if !bytes.Equal(got, want) {
		t.Fatalf("CanonicalHashKey(maxU64):\n  got:  %x\n  want: %x", got, want)
	}
}

// TestTotalDifficultyKey pins prefix 0x06 ++ 32B hash.
// Source: KeyValueStoragePrefixedKeyBlockchainStorage.java:68.
func TestTotalDifficultyKey(t *testing.T) {
	got := TotalDifficultyKey(testGenesisHash)
	want, _ := hex.DecodeString(
		"06" + "00000000000000000000000000000000000000000000000000000000000000ab",
	)
	if !bytes.Equal(got, want) {
		t.Fatalf("TotalDifficultyKey:\n  got:  %x\n  want: %x", got, want)
	}
}
