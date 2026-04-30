package reth

import "testing"

// TestRunCgoStubBuildPath verifies that without -tags cgo_reth, RunCgo's
// stub mode returns a clear error pointing at Dockerfile.reth.
//
// Skipped when built with -tags cgo_reth (the cgo build's runCgoNotAvailableError
// is nil; this test only validates the stub path).
func TestRunCgoStubBuildPath(t *testing.T) {
	if runCgoNotAvailableError == nil {
		t.Skip("built with -tags cgo_reth; stub message check is non-applicable")
	}
	msg := runCgoNotAvailableError.Error()
	if !contains(msg, "cgo_reth") {
		t.Errorf("stub error must mention 'cgo_reth' tag; got %q", msg)
	}
	if !contains(msg, "Dockerfile.reth") {
		t.Errorf("stub error must mention 'Dockerfile.reth' path; got %q", msg)
	}
}

// contains is a substring check helper local to this test file.
func contains(s, sub string) bool {
	for i := 0; i+len(sub) <= len(s); i++ {
		if s[i:i+len(sub)] == sub {
			return true
		}
	}
	return false
}
