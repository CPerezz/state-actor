package clientpolicy

import (
	"strings"
	"testing"
)

func TestValidateForClient_RecognizedClients(t *testing.T) {
	for _, c := range []string{"geth", "nethermind", "besu", "reth"} {
		if err := ValidateForClient(c, FlagValues{}); err != nil {
			t.Errorf("ValidateForClient(%q, zero FV): unexpected error: %v", c, err)
		}
	}
}

func TestValidateForClient_ErigonNotImplemented(t *testing.T) {
	err := ValidateForClient("erigon", FlagValues{})
	if err == nil || !strings.Contains(err.Error(), "not yet implemented") {
		t.Fatalf("expected 'not yet implemented' for erigon, got %v", err)
	}
}

func TestValidateForClient_UnknownRejected(t *testing.T) {
	err := ValidateForClient("frontier-of-1995", FlagValues{})
	if err == nil || !strings.Contains(err.Error(), "is not recognized") {
		t.Fatalf("expected 'not recognized' for unknown client, got %v", err)
	}
}

func TestValidateForClient_BinaryTrieGethOnly(t *testing.T) {
	if err := ValidateForClient("geth", FlagValues{BinaryTrie: true}); err != nil {
		t.Errorf("geth + --binary-trie should be allowed: %v", err)
	}
	for _, c := range []string{"nethermind", "besu", "reth"} {
		err := ValidateForClient(c, FlagValues{BinaryTrie: true})
		if err == nil || !strings.Contains(err.Error(), "EIP-7864") {
			t.Errorf("%s + --binary-trie should reject with EIP-7864 reason, got %v", c, err)
		}
	}
}

func TestValidateForClient_TargetSizeRejectsReth(t *testing.T) {
	for _, c := range []string{"geth", "nethermind", "besu"} {
		if err := ValidateForClient(c, FlagValues{TargetSize: "5GB"}); err != nil {
			t.Errorf("%s + --target-size should be allowed: %v", c, err)
		}
	}
	err := ValidateForClient("reth", FlagValues{TargetSize: "5GB"})
	if err == nil || !strings.Contains(err.Error(), "target-size") {
		t.Errorf("reth + --target-size should reject, got %v", err)
	}
}

func TestValidateForClient_ForkCeiling(t *testing.T) {
	cases := []struct {
		client   string
		fork     string
		wantPass bool
	}{
		{"geth", "prague", true},
		{"geth", "osaka", true},
		{"geth", "shanghai", true},
		{"reth", "prague", true},
		{"reth", "osaka", true},
		{"besu", "shanghai", true},
		{"besu", "cancun", true},  // Pre-C-v2: besu writer now uses internal/genesisheader.Build
		{"besu", "prague", true},
		{"besu", "osaka", true},
		{"nethermind", "merge", true},
		{"nethermind", "shanghai", true},  // Pre-C-v2: neth writer now uses internal/genesisheader.Build
		{"nethermind", "prague", true},
		{"nethermind", "osaka", true},
	}
	for _, tc := range cases {
		err := ValidateForClient(tc.client, FlagValues{Fork: tc.fork})
		if tc.wantPass && err != nil {
			t.Errorf("%s + --fork=%s: expected pass, got %v", tc.client, tc.fork, err)
		}
		if !tc.wantPass && err == nil {
			t.Errorf("%s + --fork=%s: expected reject (past ceiling), got pass", tc.client, tc.fork)
		}
	}
}
