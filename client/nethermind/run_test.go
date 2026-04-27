package nethermind

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nerolation/state-actor/generator"
)

// TestRun_StubReturnsNotImplemented pins the stage-1 scaffolding behavior:
// Run returns a clearly-labeled "not yet implemented" error so users who
// pass --client=nethermind on the current branch see what's missing
// (rather than a panic, a nil pointer, or silent geth fallback).
//
// When stage 2 lands and Run is wired up, this test gets replaced by the
// Tier 2 differential-oracle test from B6.
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
	if !strings.Contains(err.Error(), "not yet implemented") {
		t.Errorf("error text should explain status: %q", err.Error())
	}
}
