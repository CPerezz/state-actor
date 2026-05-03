package besu

import (
	"fmt"
	"os"
	"path/filepath"
)

// metadataJSON is the byte-exact V2 BONSAI version-3 metadata sidecar.
//
// Format from DatabaseMetadata.java:42 (METADATA_FILENAME = "DATABASE_METADATA.json"),
// :113-118 (V2 wrapper writes MetadataV2(format, version)), and
// BaseVersionedStorageFormat.java:45 (BONSAI_WITH_RECEIPT_COMPACTION = (BONSAI, 3)).
//
// Indentation matches Besu's Jackson INDENT_OUTPUT (DatabaseMetadata.java:47):
// 2-space indent, fields wrapped, trailing newline absent. Pinned as a literal
// string so we never silently drift if Go's json.Marshal output ordering
// changes or Besu's Jackson formatter is tweaked upstream.
const metadataJSON = `{
  "v2" : {
    "format" : "BONSAI",
    "version" : 3
  }
}`

// WriteDatabaseMetadata writes <datadir>/DATABASE_METADATA.json — the sidecar
// file Besu's RocksDBKeyValueStorageFactory.init() requires whenever the
// database/ directory exists.
//
// Constraint C2 from the plan's risk register: this MUST be written before
// the first openBesuDB call AND after the database/ directory is populated.
// Without it, Besu refuses to open the DB:
//
//	StorageException("Database exists but metadata file ... not found")
//	  RocksDBKeyValueStorageFactory.java:243-245
//
// We write it AFTER the RocksDB writes complete (last orchestrator step
// before close) so a partial run that fails mid-write doesn't leave the
// metadata claiming the DB is intact when it isn't. Combined with the
// fresh-dir precondition, this gives us a clean "either fully-written or
// not present" invariant.
func WriteDatabaseMetadata(datadir string) error {
	if err := os.MkdirAll(datadir, 0o755); err != nil {
		return fmt.Errorf("besu: mkdir datadir for metadata: %w", err)
	}
	p := filepath.Join(datadir, "DATABASE_METADATA.json")
	if err := os.WriteFile(p, []byte(metadataJSON), 0o644); err != nil {
		return fmt.Errorf("besu: write %s: %w", p, err)
	}
	return nil
}
