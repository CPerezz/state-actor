//go:build cgo_reth

package reth

import (
	"os"
	"path/filepath"
	"testing"

	iReth "github.com/nerolation/state-actor/internal/reth"
)

func TestOpenEnvsFreshDir(t *testing.T) {
	tmp := t.TempDir()
	envs, err := OpenEnvs(tmp, true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	defer envs.Close()
	if _, err := os.Stat(filepath.Join(tmp, "db", "mdbx.dat")); err != nil {
		t.Errorf("mdbx.dat not created: %v", err)
	}
	for _, ts := range iReth.Tables {
		if _, ok := envs.MdbxDBIs[ts.Name]; !ok {
			t.Errorf("missing DBI for table %q", ts.Name)
		}
	}
	for _, name := range []string{"default", "AccountsHistory", "StoragesHistory", "TransactionHashNumbers"} {
		if _, ok := envs.RocksCFs[name]; !ok {
			t.Errorf("missing RocksDB CF %q", name)
		}
	}
}

func TestOpenEnvsRefusesExistingDir(t *testing.T) {
	tmp := t.TempDir()
	dbDir := filepath.Join(tmp, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(filepath.Join(dbDir, "mdbx.dat"), []byte("preexisting"), 0o644); err != nil {
		t.Fatal(err)
	}
	_, err := OpenEnvs(tmp, true)
	if err == nil {
		t.Error("OpenEnvs(freshDir=true) should error on pre-existing mdbx.dat")
	}
}

func TestOpenEnvsCloseIsIdempotent(t *testing.T) {
	tmp := t.TempDir()
	envs, err := OpenEnvs(tmp, true)
	if err != nil {
		t.Fatalf("OpenEnvs: %v", err)
	}
	if err := envs.Close(); err != nil {
		t.Errorf("first Close: %v", err)
	}
	if err := envs.Close(); err != nil {
		t.Errorf("second Close: %v", err)
	}
}
