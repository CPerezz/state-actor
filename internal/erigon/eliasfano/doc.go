// Package eliasfano is a pure-Go implementation of Erigon's monotone-sequence
// Elias-Fano encoder, used by RecSplit internally (`.kvi` accessor's bucket-
// position index) and by inverted-index `.ef` snapshot files. The wire format
// must be byte-identical to Erigon's `db/recsplit/eliasfano32` package; the
// `cross-verify-erigon` CI job regenerates fixtures via the real Erigon
// writer and asserts `bytes.Equal`.
//
// # Spec source
//
// Mirrors `db/recsplit/eliasfano32/elias_fano.go` from Erigon v3.4.2 (see
// `internal/erigon/constants.go PinnedErigonCommit`). The file at
// `/Users/random_anon/dev/clients/erigon/db/recsplit/eliasfano32/elias_fano.go`
// is the spec reference; the citation `elias_fano.go:NNN` in this package's
// comments points at that file. Functions in this package are line-by-line
// ports of the corresponding Erigon code, with the `unsafe.Pointer` slice
// cast in `AppendBytes` (Erigon line 766) replaced by a portable
// `binary.LittleEndian` loop.
//
// # Algorithm overview
//
// Elias-Fano encodes a monotonic non-decreasing sequence of N uint64 values
// in roughly `2N + N⌈log₂(maxVal/N)⌉` bits, supporting O(1) random access
// via a jump table. See:
//   - https://www.antoniomallia.it/sorted-integers-compression-with-elias-fano-encoding.html
//   - P. Elias, "Efficient storage and retrieval by content and address of static files", J. ACM 21(2), 1974
//   - Sebastiano Vigna, "Quasi-Succinct Indices", arXiv:1206.4300
//
// # API
//
//	b := eliasfano.New(count, maxOffset)
//	for _, off := range offsets { b.AddOffset(off) }  // monotonic non-decreasing
//	b.Build()                                          // jump-table fill
//	buf = b.AppendBytes(buf)                           // serialize into buf
//	// or
//	err := b.Serialize(w)                              // serialize into io.Writer
//
// # Endianness
//
// AppendBytes / Serialize emit the underlying `[]uint64 data` slice in
// HOST byte order to match Erigon (which uses an `unsafe.Slice` reinterpret).
// State-actor's deployment is little-endian only (linux/amd64, linux/arm64,
// darwin/arm64); a runtime init() panics on big-endian platforms so a
// cross-arch mistake fails loudly rather than silently producing an
// Erigon-unreadable file.
//
// # Concurrency
//
// Builder is single-goroutine; not safe for concurrent AddOffset/Build calls.
package eliasfano
