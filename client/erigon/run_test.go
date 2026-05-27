//go:build !cgo_erigon

package erigon

import (
	"context"
	"errors"
	"strings"
	"testing"

	"github.com/nerolation/state-actor/generator"
	"github.com/nerolation/state-actor/internal/autofill"
)

// TestRun_StubReturnsNotImplemented pins the !cgo_erigon build behavior:
// Run returns a clearly-labeled error directing the user at Docker so
// `--client=erigon` on a vanilla `go build` doesn't panic, return nil, or
// silently no-op. Mirrors client/besu/run_test.go.
func TestRun_StubReturnsNotImplemented(t *testing.T) {
	// Pass a Validate-clean config (AutoFill set) so Run reaches runImpl;
	// the stub there returns errNotImplemented unconditionally.
	plan, err := autofill.PlanForBudget(512 << 10)
	if err != nil {
		t.Fatalf("PlanForBudget: %v", err)
	}
	cfg := generator.Config{AutoFill: plan, Seed: 1}
	stats, err := Run(context.Background(), cfg, Options{})
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
	// --client=erigon locally see the path forward.
	if !strings.Contains(err.Error(), "Docker") {
		t.Errorf("error text should mention Docker: %q", err.Error())
	}
	if !strings.Contains(err.Error(), "cgo_erigon") {
		t.Errorf("error text should mention cgo_erigon: %q", err.Error())
	}
}
