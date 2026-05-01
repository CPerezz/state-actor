//go:build cgo_reth

package reth

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/ethereum/go-ethereum/common"

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

func TestRunCgoSyntheticEOAs(t *testing.T) {
	tmp := t.TempDir()
	cfg := generator.Config{
		DBPath:      tmp,
		NumAccounts: 50,
		Seed:        12345,
	}
	stats, err := RunCgo(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("RunCgo: %v", err)
	}
	if stats == nil {
		t.Fatal("RunCgo returned nil stats")
	}
	if stats.AccountsCreated != 50 {
		t.Errorf("AccountsCreated = %d, want 50", stats.AccountsCreated)
	}
	emptyRoot := common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	if stats.StateRoot == emptyRoot {
		t.Error("state root should differ from empty-MPT hash for non-empty alloc")
	}
	for _, p := range []string{"db/mdbx.dat", "db/database.version", "chainspec.json", "rocksdb/CURRENT"} {
		if _, err := os.Stat(filepath.Join(tmp, p)); err != nil {
			t.Errorf("expected %s: %v", p, err)
		}
	}
}

func TestRunCgoSyntheticEOAsDeterminism(t *testing.T) {
	// Same seed → same state root.
	cfg := generator.Config{NumAccounts: 25, Seed: 9999}
	tmp1 := t.TempDir()
	cfg.DBPath = tmp1
	s1, err := RunCgo(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("first run: %v", err)
	}
	tmp2 := t.TempDir()
	cfg.DBPath = tmp2
	s2, err := RunCgo(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("second run: %v", err)
	}
	if s1.StateRoot != s2.StateRoot {
		t.Errorf("non-deterministic state root: %s vs %s", s1.StateRoot.Hex(), s2.StateRoot.Hex())
	}
}

func TestRunCgoSyntheticContracts(t *testing.T) {
	tmp := t.TempDir()
	cfg := generator.Config{
		DBPath:       tmp,
		NumAccounts:  20,
		NumContracts: 5,
		Seed:         42,
	}
	stats, err := RunCgo(context.Background(), cfg, Options{})
	if err != nil {
		t.Fatalf("RunCgo: %v", err)
	}
	if stats == nil {
		t.Fatal("RunCgo returned nil stats")
	}
	if stats.AccountsCreated != 20 {
		t.Errorf("AccountsCreated = %d, want 20", stats.AccountsCreated)
	}
	if stats.ContractsCreated != 5 {
		t.Errorf("ContractsCreated = %d, want 5", stats.ContractsCreated)
	}
	emptyRoot := common.HexToHash("0x56e81f171bcc55a6ff8345e692c0f86e5b48e01b996cadc001622fb5e363b421")
	if stats.StateRoot == emptyRoot {
		t.Error("state root should differ from empty-MPT for non-empty alloc")
	}

	// Verify static-files segments exist (paths per Task 4's findings).
	for _, p := range []string{
		"static_files",
		"db/mdbx.dat",
		"db/database.version",
		"chainspec.json",
		"rocksdb/CURRENT",
	} {
		if _, err := os.Stat(filepath.Join(tmp, p)); err != nil {
			t.Errorf("expected %s: %v", p, err)
		}
	}
}

func TestRunCgoContractsDeterminism(t *testing.T) {
	cfg := generator.Config{NumAccounts: 10, NumContracts: 3, Seed: 1234}
	var roots [2]common.Hash
	for i := range roots {
		tmp := t.TempDir()
		cfg.DBPath = tmp
		s, err := RunCgo(context.Background(), cfg, Options{})
		if err != nil {
			t.Fatalf("run %d: %v", i, err)
		}
		roots[i] = s.StateRoot
	}
	if roots[0] != roots[1] {
		t.Errorf("non-deterministic state root: %s vs %s", roots[0].Hex(), roots[1].Hex())
	}
}
