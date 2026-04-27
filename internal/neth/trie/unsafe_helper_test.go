package trie

import "unsafe"

// unsafePointer is a tiny wrapper that scopes the unsafe import to a
// single test helper (used only by builder_test.go's deep-copy invariant
// check). Not part of the package's public surface.
func unsafePointer(p *byte) unsafe.Pointer { return unsafe.Pointer(p) }
