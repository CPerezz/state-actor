package keys

import "testing"

// TestSentinelKeyLengths pins the byte lengths of all sentinel keys.
// These lengths match the Java source strings exactly. A length mismatch
// means the key was mis-spelled and would never be found by Besu.
func TestSentinelKeyLengths(t *testing.T) {
	cases := []struct {
		name string
		key  []byte
		want int
	}{
		// TRIE_BRANCH_STORAGE sentinels
		{"WorldRootKey", WorldRootKey, 9},
		{"WorldBlockHashKey", WorldBlockHashKey, 14},
		{"WorldBlockNumberKey", WorldBlockNumberKey, 16},
		{"FlatDbStatusKey", FlatDbStatusKey, 12},
		// VARIABLES sentinel
		{"ChainHeadHashKey", ChainHeadHashKey, 13},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if len(c.key) != c.want {
				t.Fatalf("%s: len=%d, want %d (bytes: %x)",
					c.name, len(c.key), c.want, c.key)
			}
		})
	}
}

// TestCFByteIDs pins the single-byte CF identifiers.
// CF names are raw bytes 1, 6..11 — NOT the ASCII characters '1', '6'.
func TestCFByteIDs(t *testing.T) {
	cases := []struct {
		name string
		cf   []byte
		want byte
	}{
		{"CFBlockchain", CFBlockchain, 0x01},
		{"CFAccountInfoState", CFAccountInfoState, 0x06},
		{"CFCodeStorage", CFCodeStorage, 0x07},
		{"CFAccountStorageStorage", CFAccountStorageStorage, 0x08},
		{"CFTrieBranchStorage", CFTrieBranchStorage, 0x09},
		{"CFTrieLogStorage", CFTrieLogStorage, 0x0a},
		{"CFVariables", CFVariables, 0x0b},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if len(c.cf) != 1 {
				t.Fatalf("%s: len=%d, want 1 (bytes: %x)", c.name, len(c.cf), c.cf)
			}
			if c.cf[0] != c.want {
				t.Fatalf("%s: byte=%#x, want %#x", c.name, c.cf[0], c.want)
			}
		})
	}
}

// TestCFDefault pins the "default" CF as UTF-8 string, NOT a single byte.
func TestCFDefault(t *testing.T) {
	want := "default"
	if string(CFDefault) != want {
		t.Fatalf("CFDefault: got %q, want %q", string(CFDefault), want)
	}
}

// TestFlatDbStatusFull pins the FlatDbMode FULL byte and the genesis block
// number sentinel value length.
func TestFlatDbStatusFull(t *testing.T) {
	if len(FlatDbStatusFull) != 1 || FlatDbStatusFull[0] != 0x01 {
		t.Fatalf("FlatDbStatusFull: got %x, want [0x01]", FlatDbStatusFull)
	}
	if len(WorldBlockNumberGenesis) != 8 {
		t.Fatalf("WorldBlockNumberGenesis: len=%d, want 8", len(WorldBlockNumberGenesis))
	}
	for i, b := range WorldBlockNumberGenesis {
		if b != 0 {
			t.Fatalf("WorldBlockNumberGenesis[%d]=%x, want 0x00", i, b)
		}
	}
}
