// Package recsplit is a pure-Go implementation of Erigon's RecSplit
// minimal-perfect-hash writer, producing byte-identical `.kvi` accessor
// files used by Erigon's commitment domain (only).
//
// In production Erigon v3, only the commitment domain uses RecSplit (`.kvi`)
// accessors. Value domains (accounts/storage/code) use B+tree (`.bt`) accessors.
// state-actor needs RecSplit because the Verifier-A correction (plan section
// "Correction 1") requires shipping the commitment domain: the Erigon daemon
// does not call RebuildCommitmentFilesWithHistory at startup, so commitment
// must be pre-built.
//
// # Spec source
//
// Mirrors `db/recsplit/recsplit.go` + `db/recsplit/golomb_rice.go` +
// `db/recsplit/eliasfano16/elias_fano.go` from Erigon v3.4.2 (see
// `internal/erigon/constants.go PinnedErigonCommit`). The file at
// `/Users/random_anon/dev/clients/erigon/db/recsplit/recsplit.go` is the
// spec reference; citations like `recsplit.go:NNN` in this package's
// comments point at that file. Functions are line-by-line ports with the
// `unsafe.Pointer` slice-cast in `GolombRice.Write` (Erigon line 191) and
// `DoubleEliasFano.Write` (eliasfano16/elias_fano.go:524) replaced by a
// portable `binary.LittleEndian.PutUint64` loop.
//
// # Algorithm overview
//
// RecSplit (Esposito, Mueller Graf, Vigna 2020, ALENEX) builds a minimal
// perfect hash function over N keys using recursive splitting:
//
//  1. Hash each key with murmur3 seeded by `salt`, take the lo 64 bits as
//     the "fingerprint" and `remap(hi, bucketCount)` as the bucket index.
//  2. Sort (bucketIdx, fingerprintLo) → bucket-grouped fingerprint stream.
//  3. For each bucket: find a "bijection" salt s such that hashing each
//     fingerprint with s remixes them to distinct slots in [0, m). The
//     salt s is then Golomb-Rice encoded (its low bits inline; its high
//     bits as a unary prefix in the global GolombRice bit-stream).
//  4. For buckets larger than `leafSize` (default 8): recursively split
//     into `fanout` partitions of size `unit` each via `findSplit`; encode
//     the partition-salt the same way; recurse on each partition.
//  5. Build a DoubleEliasFano index over (bucketSizeAccumulator,
//     bucketBitPositionAccumulator) so a reader can locate any bucket's
//     Golomb-Rice slice in O(1).
//
// The resulting `.kvi` is laid out as:
//
//	header:   1B dataStructureVersion | 7B baseDataID
//	          8B keyCount
//	          1B bytesPerRec
//	body:     keyCount * bytesPerRec bytes of (bucket-order-shuffled) offsets
//	footer:   8B bucketCount
//	          2B bucketSize
//	          2B leafSize
//	          4B salt
//	          1B startSeedLen | startSeedLen * 8B startSeeds
//	          1B Features byte
//	          (if enums: 8B+8B+8B+data eliasfano32 offsetEf)
//	          (if lessFalsePositives: existence filter blob)
//	          4B golombRice param count (uint16 in low 2 BE bytes, +2 zero)
//	          8B grData len | grData len * 8B grData (host-LE)
//	          DoubleEliasFano (40B header + N * 8B data, host-LE)
//
// # API
//
//	w, err := recsplit.New(recsplit.Args{
//	    KeyCount:   N,
//	    BucketSize: 100,
//	    Salt:       &salt,       // pointer, mutable across collision retries
//	    LeafSize:   8,
//	    TmpDir:     "/tmp",
//	    IndexFile:  "/path/v1.0-commitments.0-256.kvi",
//	    BaseDataID: meta.First,
//	})
//	for ... { w.AddKey(key, offset) }
//	if err := w.Build(ctx); err != nil { /* may return ErrCollision; caller bumps salt and retries */ }
//	w.Close()
//
// # Endianness
//
// `.kvi` emits internal uint64 arrays (`grData`, `ef.data`) in HOST byte
// order to match Erigon (which uses `unsafe.Pointer` slice reinterpret).
// state-actor's deployment is little-endian only; a runtime `init()`
// panics on big-endian platforms.
//
// # Concurrency
//
// Single-goroutine writer; not safe for concurrent AddKey/Build calls.
// state-actor's v1 deliberately mirrors Erigon's sequential (workers=1)
// path — the parallel-build path is correctness-critical and out of
// scope for the spike.
package recsplit
