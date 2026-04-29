package reth

import "testing"

func TestPinnedConstants(t *testing.T) {
	if PinnedCodecsVer != "0.3.1" {
		t.Errorf("PinnedCodecsVer = %q, want %q", PinnedCodecsVer, "0.3.1")
	}
	if PinnedAlloyTrieVer != "0.9.5" {
		t.Errorf("PinnedAlloyTrieVer = %q, want %q", PinnedAlloyTrieVer, "0.9.5")
	}
	if PinnedMdbxGoVer != "v0.38.4" {
		t.Errorf("PinnedMdbxGoVer = %q, want %q", PinnedMdbxGoVer, "v0.38.4")
	}
	if DBVersion != 2 {
		t.Errorf("DBVersion = %d, want 2", DBVersion)
	}
	if PinnedRethCommit == "" {
		t.Error("PinnedRethCommit must not be empty")
	}
	if PinnedRethRelease == "" {
		t.Error("PinnedRethRelease must not be empty")
	}
}
