package snap

import (
	"fmt"
	"path/filepath"
)

// E3 file-naming template per
// /Users/random_anon/dev/clients/erigon/db/state/snap_schema.go:441,467.
// Steps are NOT zero-padded; version is a PREFIX.
//
//	"<version>-<tag>.<from>-<to><ext>"
const e3Template = "%s-%s.%d-%d%s"

// File extensions for the four artifact types a domain may emit.
const (
	ExtKV   = ".kv"   // data: Huffman-encoded (key, value) stream
	ExtBT   = ".bt"   // accessor: BTree (value-domain default)
	ExtKVI  = ".kvi"  // accessor: RecSplit perfect hash (commitment-domain only)
	ExtKVEI = ".kvei" // accessor: bloom existence filter
)

// BuildDataFilename returns the absolute path of the .kv data file for
// (domain, range) under dir, using version + the E3 template.
//
//	dir/v1.0-accounts.0-256.kv
func BuildDataFilename(dir, version string, d Domain, r StepRange) string {
	return filepath.Join(dir, fmt.Sprintf(e3Template, version, d.Tag(), r.From, r.To, ExtKV))
}

// BuildBTreeFilename returns the absolute path of the .bt accessor.
//
//	dir/v1.0-accounts.0-256.bt
func BuildBTreeFilename(dir, version string, d Domain, r StepRange) string {
	return filepath.Join(dir, fmt.Sprintf(e3Template, version, d.Tag(), r.From, r.To, ExtBT))
}

// BuildHashMapFilename returns the absolute path of the .kvi
// (RecSplit) accessor. Commitment-domain only.
//
//	dir/v1.0-commitments.0-256.kvi
func BuildHashMapFilename(dir, version string, d Domain, r StepRange) string {
	return filepath.Join(dir, fmt.Sprintf(e3Template, version, d.Tag(), r.From, r.To, ExtKVI))
}

// BuildExistenceFilename returns the absolute path of the .kvei
// bloom-filter accessor.
//
//	dir/v1.0-accounts.0-256.kvei
func BuildExistenceFilename(dir, version string, d Domain, r StepRange) string {
	return filepath.Join(dir, fmt.Sprintf(e3Template, version, d.Tag(), r.From, r.To, ExtKVEI))
}
