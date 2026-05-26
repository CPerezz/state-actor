// Package seg is a pure-Go port of Erigon's `db/seg` package — the
// Huffman-coded body writer/reader for `.kv`, `.v`, and `.ef` snapshot
// files. State-actor's Erigon support is "Architect B: own the format",
// so this package emits byte-identical output to Erigon's seg.Compressor
// (verified by the `cross-verify-erigon` CI job).
//
// # Spec source
//
// Mirrors three Erigon files at v3.4.2 (see
// `internal/erigon/constants.go PinnedErigonCommit`):
//
//   - `db/seg/compress.go` (Compressor + dictionary + RawWordsFile)
//   - `db/seg/parallel_compress.go` (Huffman build + bitstream encoding)
//   - `db/seg/decompress.go` (Decompressor + Getter for the round-trip
//     reader half).
//
// Citations of the form `compress.go:NNN` in this package's comments
// refer to those files under
// `/Users/random_anon/dev/clients/erigon/db/seg/`.
//
// # Wire format (V1, no-pattern fast path)
//
// V1 header (3 bytes):
//
//	byte[0] = version = 1
//	byte[1] = featureFlagBitmask (bit0: PageLevelCompressionEnabled,
//	          bit1: KeyCompressionEnabled, bit2: ValCompressionEnabled)
//	(byte[2] = compPageValuesCount if PageLevelCompressionEnabled — skipped in v1)
//
// Data section (`compressNoWordPatterns` path, parallel_compress.go:684-754):
//
//	8B BE: wordsCount
//	8B BE: emptyWordsCount
//	8B BE: patternsSize  // 0 in fast path (no pattern dict)
//	patternsSize bytes: pattern dictionary entries
//	                    (each: varint(depth), varint(len(word)), word bytes)
//	8B BE: posDictSize
//	posDictSize bytes: position dictionary entries
//	                   (each: varint(depth), varint(pos))
//	Huffman-encoded data section:
//	  for each word w of length L:
//	    bits: pos2code[L+1].code  (encoded length code)
//	    if L == 0: flush (byte-align)
//	    else:
//	      bits: pos2code[0].code   (terminator code)
//	      flush (byte-align)
//	      raw L bytes of w
//
// # Two-pass writer pattern (Verifier Correction 2)
//
// `Compressor.AddWord` cannot return the final `.kv` byte offset because
// Huffman + dictionary encoding happens in `Compress()` AFTER all words
// are added. Downstream accessor builders (BTree, RecSplit, Bloom) need
// per-key offsets to populate their key→offset mappings. The pattern,
// per Erigon's `db/state/simple_accessor_builder.go:194-216`, is two-pass:
//
//	// Pass 1: write the .kv file
//	c, _ := seg.NewCompressor(outPath, tmpDir, cfg)
//	c.AddWord(k1); c.AddWord(v1); c.AddWord(k2); c.AddWord(v2); ...
//	c.Compress()
//	c.Close()
//
//	// Pass 2: iterate to discover offsets
//	d, _ := seg.NewDecompressor(outPath)
//	for entry, err := range d.Iterate(ctx) {
//	    if err != nil { ... }
//	    // entry.KeyOffset / entry.ValueOffset are the post-compression byte offsets
//	    btree.AddKey(entry.Key, entry.ValueOffset)
//	}
//	d.Close()
//
// # v1 scope (no-pattern fast path)
//
// For state-actor's first 10/100/1K-word fixtures with Erigon's default
// `MinPatternScore = 1024`, the dictionary-pattern extraction phase
// (parallel_compress.go:967-1029) produces zero patterns because no
// 5+-byte substring occurs ≥1024 times in 10–1K random key/value pairs.
// In this regime `Compressor.Compress` takes the `compressNoWordPatterns`
// fast path (compress.go:359-368), and we produce byte-identical output
// without needing a Patricia tree, MatchFinder3, or the recursive
// dynamic-programming cover algorithm.
//
// Inputs that would actually populate the pattern dictionary (highly
// repetitive 100K+ word inputs) are out of scope for v1. The plan
// (Part 1a Task 10) gates that test under `-short=false`; this package
// implements only the small-input path. The Patricia-tree pattern-cover
// implementation is deferred to v2.
//
// # Algorithm sketch
//
// 1. AddWord: append (varint(2*len), bytes) to a temp .idt file.
// 2. Compress: scan .idt twice.
//   - Pass A: build posMap[length+1] = uses (count of words of each length);
//     posMap[0] = totalWordCount (terminator).
//   - Pass B: build canonical Huffman tree over posMap. Write 3×8B header
//     (wordsCount, emptyWordsCount, patternsSize=0), then 8B posSize
//     followed by varint-encoded (depth, pos) pairs. Then Huffman-encode
//     each word's length+terminator codes and flush+raw-bytes.
//
// 3. Decompressor.Iterate: replay the same Huffman decode to discover
//    each word's byte boundaries. Word offset = byte offset just BEFORE
//    the encoded length code (i.e., immediately after the previous word's
//    raw bytes).
//
// # Pure-Go constraint
//
// No cgo (the main state-actor module forbids importing
// `github.com/erigontech/erigon`). The fixture-generation submodule at
// `internal/erigon/_fixtures/cmd/seg` does import Erigon, but is built
// under the `erigon_gen` build tag so untagged `go build ./...` skips it.
//
// # Concurrency
//
// Compressor and Decompressor are single-goroutine. Multiple
// Decompressors may operate concurrently on the same file (mmap is
// shared via the OS page cache).
package seg
