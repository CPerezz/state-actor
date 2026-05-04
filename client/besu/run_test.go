//go:build !cgo_besu

package besu

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nerolation/state-actor/generator"
)

// TestRun_StubReturnsNotImplemented pins the !cgo_besu build behavior:
// Run returns a clearly-labeled error directing the user at Docker so
// `--client=besu` on a vanilla `go build` doesn't panic, return nil, or
// silently no-op.
//
// Skipped when built with -tags cgo_besu — that path is exercised by the
// differential-oracle test inside the Docker context.
func TestRun_StubReturnsNotImplemented(t *testing.T) {
	stats, err := Run(context.Background(), generator.Config{}, Options{})
	if err == nil {
		t.Fatal("Run returned nil error; expected stub error")
	}
	if !errors.Is(err, errNotImplemented) {
		t.Fatalf("expected errNotImplemented, got %v", err)
	}
	if stats != nil {
		t.Errorf("expected nil stats from stub, got %#v", stats)
	}
	// The user-facing message must point at Docker so users who try
	// --client=besu locally see the path forward.
	if !strings.Contains(err.Error(), "Docker") {
		t.Errorf("error text should mention Docker: %q", err.Error())
	}
}
