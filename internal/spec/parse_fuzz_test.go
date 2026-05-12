package spec

import (
	"strings"
	"testing"
)

// FuzzParse runs Parse on arbitrary input to catch panics. Native Go fuzz
// (Go 1.18+) is opt-in via `go test -fuzz=FuzzParse`; the default test run
// only exercises the seed corpus below, which is cheap.
//
// The contract this fuzz test pins: Parse never panics. It is allowed (and
// expected) to return an error for malformed input.
func FuzzParse(f *testing.F) {
	// Seed corpus — every shape this package needs to handle.
	seeds := []string{
		"",
		"entities: []",
		`entities:
  - kind: eoa
    address: 0x1111111111111111111111111111111111111111`,
		`entities:
  - kind: contract
    template: erc20
    name: foo`,
		`entities:
  - kind: contract
    code: "0x6080"
    approximate_size_bytes: 1000000`,
		// Pathological inputs.
		"!!!",
		"---\n---\n",
		"entities: { not a list }",
		"entities:\n  - kind: 12345",
	}
	for _, s := range seeds {
		f.Add(s)
	}

	f.Fuzz(func(t *testing.T, input string) {
		// Parse must never panic; errors are fine.
		_, _ = Parse(strings.NewReader(input))
	})
}
