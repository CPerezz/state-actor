package seg

// File-format constants and helpers shared by Compressor and Decompressor.
// Citations refer to `/Users/random_anon/dev/clients/erigon/db/seg/`.

// FileCompression mirrors `db/seg/seg_interface.go:19-25`. Bits indicate
// which words (keys, values, neither, or both) are subject to the
// pattern-dictionary compression. State-actor v1 only uses CompressNone
// for the `.kv` writer path (the snapshot-domain caller picks per-domain
// compression in Part 2).
type FileCompression uint8

const (
	// CompressNone — emit every word via AddUncompressedWord. Used for
	// history .v files. Erigon's CompressNone is `0b1` (compress.go:22),
	// but state-actor uses `0` to keep semantic "default no compression"
	// the zero value. The Compressor honors the legacy `0b1` encoding
	// internally for cross-format parity.
	CompressNone FileCompression = 0
	// CompressKeys — alternating words: even index keys go through
	// AddWord (pattern-compressed), odd index values go through
	// AddUncompressedWord. Matches Erigon's `0b10` mask.
	CompressKeys FileCompression = 1
	// CompressVals — even-index keys uncompressed, odd-index values
	// pattern-compressed. Matches Erigon's `0b100` mask.
	CompressVals FileCompression = 2
)

// Has reports whether the mask sets the given flag.
func (c FileCompression) Has(flag FileCompression) bool {
	return c&flag != 0
}

// featureFlagBitmask is the packed byte at file offset 1 (the second
// byte of the V1 header). Bits, matching `db/seg/seg_interface.go:32-38`:
//
//	bit 0: PageLevelCompressionEnabled (followed by 1-byte compPageValuesCount)
//	bit 1: KeyCompressionEnabled
//	bit 2: ValCompressionEnabled
//
// state-actor v1 always writes 0 (none of these features are implemented).
// NewDecompressor rejects any file with non-zero featureFlagBitmask to
// avoid silently mis-decoding extended-format files.
type featureFlagBitmask uint8

// fileCompressionFormatV1 is the version byte at file offset 0 of V1
// files. Mirrors `db/seg/seg_interface.go:29 FileCompressionFormatV1`.
// V0 had no version byte (data started immediately); state-actor only
// emits and reads V1.
const fileCompressionFormatV1 uint8 = 1

// compressedMinSize is Erigon's sanity-check at decompress.go:207.
// A file smaller than 32 bytes can't contain the 8+8+8+8 (count, empty,
// patternsSize, posSize) header alone.
const compressedMinSize = 32

// Default config values from `db/seg/compress.go:90-99 DefaultCfg`.
const (
	defaultMinPatternScore uint64 = 1024
	defaultMinPatternLen          = 5
	defaultMaxPatternLen          = 128
	defaultMaxDictPatterns        = 64 * 1024
)
