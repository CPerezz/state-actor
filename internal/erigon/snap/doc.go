// Package snap composes the pure-Go writer primitives at
// internal/erigon/{seg,btindex,recsplit,existence} into Erigon's
// per-domain snapshot file set ({domain}.{from}-{to}.{kv,bt,kvi,kvei}).
//
// File-naming follows the E3 template at
// erigontech/erigon/db/state/snap_schema.go:441,467:
//
//	"<version>-<tag>.<from>-<to><ext>"
//
// e.g. "v1.0-accounts.0-256.kv". Steps are NOT zero-padded; version
// prefix is required.
//
// WriteDomain is two-pass per Verifier B's correction (see plan's
// Critical Verifier Corrections > Correction 2):
//
//  1. Stream entries through seg.Compressor, calling Compress()+Close().
//  2. Re-open the .kv via seg.Decompressor.Iterate() to recover the
//     keyOff/valueOff pairs; feed them into the chosen accessor mix
//     (btindex.Writer + existence.FilterBuilder for value domains,
//     recsplit.Writer + existence.FilterBuilder for the commitment
//     domain).
//
// Accessors are per-domain (Verifier B's correction):
//   - DomainAccounts/Storage/Code → AccessorBTree | AccessorExistence
//   - DomainCommitment            → AccessorHashMap | AccessorExistence
//
// Architect B invariant: this package does NOT import
// github.com/erigontech/erigon. All schema constants are mirrored from
// the pinned Erigon source under erigontech/erigon/
// (see internal/erigon/constants.go for the pinning policy).
package snap
