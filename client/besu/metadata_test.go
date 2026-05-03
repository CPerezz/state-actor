package besu

import (
	"os"
	"path/filepath"
	"testing"
)

// TestWriteDatabaseMetadata_ExactJSON verifies the byte-exact content of
// DATABASE_METADATA.json matches the BONSAI v3 schema Besu expects.
//
// Source: DatabaseMetadata.java:113-118 + BaseVersionedStorageFormat.java:45.
func TestWriteDatabaseMetadata_ExactJSON(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDatabaseMetadata(dir); err != nil {
		t.Fatalf("WriteDatabaseMetadata: %v", err)
	}
	got, err := os.ReadFile(filepath.Join(dir, "DATABASE_METADATA.json"))
	if err != nil {
		t.Fatalf("ReadFile: %v", err)
	}
	want := `{
  "v2" : {
    "format" : "BONSAI",
    "version" : 3
  }
}`
	if string(got) != want {
		t.Fatalf("metadata content mismatch:\n  got:  %q\n  want: %q",
			string(got), want)
	}
}

// TestWriteDatabaseMetadata_CreatesDir verifies that the function creates
// the datadir if it doesn't already exist.
func TestWriteDatabaseMetadata_CreatesDir(t *testing.T) {
	parent := t.TempDir()
	dir := filepath.Join(parent, "subdir", "nested")
	if err := WriteDatabaseMetadata(dir); err != nil {
		t.Fatalf("WriteDatabaseMetadata: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dir, "DATABASE_METADATA.json")); err != nil {
		t.Fatalf("metadata file not present at %s: %v", dir, err)
	}
}

// TestWriteDatabaseMetadata_Overwrite ensures a second call overwrites the
// existing file (idempotent — useful for test re-runs).
func TestWriteDatabaseMetadata_Overwrite(t *testing.T) {
	dir := t.TempDir()
	if err := WriteDatabaseMetadata(dir); err != nil {
		t.Fatalf("first write: %v", err)
	}
	// Corrupt the file.
	if err := os.WriteFile(filepath.Join(dir, "DATABASE_METADATA.json"), []byte("garbage"), 0o644); err != nil {
		t.Fatalf("corrupt: %v", err)
	}
	if err := WriteDatabaseMetadata(dir); err != nil {
		t.Fatalf("second write: %v", err)
	}
	got, _ := os.ReadFile(filepath.Join(dir, "DATABASE_METADATA.json"))
	if string(got) == "garbage" {
		t.Fatal("second write did not overwrite corrupt content")
	}
}
