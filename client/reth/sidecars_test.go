package reth

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func TestWriteDatabaseVersion(t *testing.T) {
	tmp := t.TempDir()
	dbDir := filepath.Join(tmp, "db")
	if err := os.MkdirAll(dbDir, 0o755); err != nil {
		t.Fatal(err)
	}
	if err := WriteDatabaseVersion(dbDir); err != nil {
		t.Fatalf("WriteDatabaseVersion: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dbDir, "database.version"))
	if err != nil {
		t.Fatal(err)
	}
	want := "2"
	if strings.TrimSpace(string(got)) != want {
		t.Errorf("database.version contents = %q, want %q", string(got), want)
	}
}
