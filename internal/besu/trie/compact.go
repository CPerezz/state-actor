// Package trie implements the Besu Bonsai path-keyed Merkle Patricia Trie
// (MPT) builder in pure Go.
//
// Bonsai reuses standard Ethereum MPT node types (Branch / Leaf / Extension /
// Null) with `keccak256(RLP(node))` hashing and the `RLP < 32` inline rule.
// The Bonsai novelty is **path-keyed storage**: trie nodes are written under
// a key equal to the raw nibble path from root (one byte per nibble). This
// is distinct from CompactEncoding (which is the standard HP-encoding of
// nibble paths *inside* node RLP for leaf/extension prefixes).
//
// Citation: hyperledger/besu tag 26.5.0.
package trie

// CompactEncode implements Besu's CompactEncoding.encode (HP-encoding) used
// inside leaf and extension node RLP. This is NOT the DB-key encoding —
// see location.go for that.
//
// HP metadata byte layout (CompactEncoding.java:79-112):
//
//	0x00 + (highNibble<<4)  if even-length extension (highNibble forced to 0)
//	0x10 + firstNibble       if odd-length extension
//	0x20 + (highNibble<<4)  if even-length leaf (highNibble forced to 0)
//	0x30 + firstNibble       if odd-length leaf
//
// Output length: len(nibbles)/2 + 1.
//
// Each nibble in the input must be in [0, 15]. The leaf terminator (0x10) is
// signaled by the isLeaf flag, NOT by appending it to the nibble slice.
func CompactEncode(nibbles []byte, isLeaf bool) []byte {
	odd := len(nibbles)%2 == 1

	var meta byte
	switch {
	case isLeaf && odd:
		meta = 0x30 | nibbles[0]
	case isLeaf && !odd:
		meta = 0x20
	case !isLeaf && odd:
		meta = 0x10 | nibbles[0]
	default:
		meta = 0x00
	}

	var remaining []byte
	if odd {
		remaining = nibbles[1:]
	} else {
		remaining = nibbles
	}

	out := make([]byte, 1+len(remaining)/2)
	out[0] = meta
	for i := 0; i < len(remaining); i += 2 {
		out[1+i/2] = (remaining[i] << 4) | remaining[i+1]
	}
	return out
}

// CompactDecode is the inverse of CompactEncode. Returns the nibble slice
// (without leaf terminator) and the isLeaf flag.
//
// Used only for round-trip verification in tests; the production builder does
// not need to decode HP-encoded paths.
func CompactDecode(encoded []byte) (nibbles []byte, isLeaf bool) {
	if len(encoded) == 0 {
		panic("CompactDecode: empty input")
	}
	meta := encoded[0]
	flag := meta >> 4
	switch flag {
	case 0x0:
		isLeaf = false
		// even-length extension; low nibble of meta is reserved zero
	case 0x1:
		isLeaf = false
		nibbles = append(nibbles, meta&0x0f)
	case 0x2:
		isLeaf = true
		// even-length leaf
	case 0x3:
		isLeaf = true
		nibbles = append(nibbles, meta&0x0f)
	default:
		panic("CompactDecode: invalid metadata byte")
	}

	for _, b := range encoded[1:] {
		nibbles = append(nibbles, b>>4, b&0x0f)
	}
	return nibbles, isLeaf
}
