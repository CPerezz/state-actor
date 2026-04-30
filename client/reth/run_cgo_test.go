//go:build cgo_reth

package reth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/nerolation/state-actor/generator"
)

func TestRunCgoEmptyAlloc(t *testing.T) {
	tmp := t.TempDir()
	cfg := generator.Config{
		DBPath: tmp,
	}
	stats, err := RunCgo(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("RunCgo: %v", err)
	}
	if stats == nil {
		t.Fatal("RunCgo returned nil stats")
	}
	for _, p := range []string{"db/mdbx.dat", "db/database.version", "chainspec.json", "rocksdb/CURRENT"} {
		if _, err := os.Stat(filepath.Join(tmp, p)); err != nil {
			t.Errorf("expected %s: %v", p, err)
		}
	}
	want := "0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421"
	if got := stats.StateRoot.Hex(); got != want {
		t.Errorf("state root: got=%s want=%s", got, want)
	}
}
