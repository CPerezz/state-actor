// Package trie holds the Nethermind-shaped TrieNode and HexPrefix
// encoders. It does NOT implement the trie itself — that's done by
// internal/neth/trie/builder.go which wraps go-ethereum's StackTrie.
package trie

// EncodeHexPrefix produces the HP-encoded byte representation of a nibble
// path, matching Nethermind's `Nethermind.Trie.HexPrefix.CopyToSpan` byte for
// byte (Nethermind upstream/master: src/Nethermind/Nethermind.Trie/HexPrefix.cs).
//
// Layout:
//   - Output length: len(path)/2 + 1
//   - First byte:    high bit (0x20) is the leaf flag.
//                    For odd-length paths, also 0x10 is set and the first
//                    nibble lives in the low 4 bits.
//   - Subsequent bytes: pair adjacent nibbles into a single byte
//                       (high nibble first), starting from path[0] for
//                       even-length paths and path[1] for odd-length.
//
// nibbles must each be in [0, 15]. Behavior for out-of-range bytes is
// undefined (Nethermind's encoder happily produces garbage there too).
func EncodeHexPrefix(path []byte, isLeaf bool) []byte {
	out := make([]byte, len(path)/2+1)

	if isLeaf {
		out[0] = 0x20
	}
	odd := len(path)%2 != 0
	if odd {
		out[0] += 0x10 + path[0]
	}

	// Loop body mirrors HexPrefix.cs:30-37 verbatim.
	//
	// for (int i = 0; i < path.Length - 1; i += 2) {
	//     output[i / 2 + 1] =
	//         path.Length % 2 == 0
	//             ? (byte)(16 * path[i] + path[i + 1])
	//             : (byte)(16 * path[i + 1] + path[i + 2]);
	// }
	for i := 0; i < len(path)-1; i += 2 {
		if odd {
			out[i/2+1] = 16*path[i+1] + path[i+2]
		} else {
			out[i/2+1] = 16*path[i] + path[i+1]
		}
	}
	return out
}
