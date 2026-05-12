package spec

import (
	"strings"
	"testing"
)

func TestParseValidStory1(t *testing.T) {
	s, err := ParseFile("testdata/valid-story1.yaml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got, want := len(s.Entities), 8; got != want {
		t.Fatalf("entity count: got %d, want %d", got, want)
	}
	// Story 1 starts with three ERC-20 templates of decreasing size.
	for i, want := range []uint64{10_000_000_000, 5_000_000_000, 1_000_000_000} {
		if got := s.Entities[i].ApproximateSizeBytes; got != want {
			t.Errorf("entity[%d] approximate_size_bytes: got %d, want %d", i, got, want)
		}
		if s.Entities[i].Template != "erc20" {
			t.Errorf("entity[%d] template: got %q, want erc20", i, s.Entities[i].Template)
		}
	}
	// The last five are EIP-7702 EOAs.
	for i := 3; i < 8; i++ {
		if s.Entities[i].Kind != KindEOA {
			t.Errorf("entity[%d] kind: got %q, want eoa", i, s.Entities[i].Kind)
		}
		if len(s.Entities[i].Code) != 23 {
			t.Errorf("entity[%d] 7702 code length: got %d, want 23", i, len(s.Entities[i].Code))
		}
	}
}

func TestParseValidStory2(t *testing.T) {
	s, err := ParseFile("testdata/valid-story2.yaml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got, want := len(s.Entities), 3; got != want {
		t.Fatalf("entity count: got %d, want %d", got, want)
	}
	// All three entities are bloated EOAs with 7702 delegation + storage.
	for i, want := range []uint64{10_000_000_000, 5_000_000_000, 2_000_000_000} {
		e := s.Entities[i]
		if e.Kind != KindEOA {
			t.Errorf("entity[%d] kind: got %q, want eoa", i, e.Kind)
		}
		if e.ApproximateSizeBytes != want {
			t.Errorf("entity[%d] approximate_size_bytes: got %d, want %d", i, e.ApproximateSizeBytes, want)
		}
		if len(e.Code) != 23 {
			t.Errorf("entity[%d] code length: got %d, want 23", i, len(e.Code))
		}
	}
}

func TestParseValidAllFeatures(t *testing.T) {
	s, err := ParseFile("testdata/valid-all-features.yaml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if got, want := len(s.Entities), 7; got != want {
		t.Fatalf("entity count: got %d, want %d", got, want)
	}
	// Address resolution mode coverage: explicit, name-derived, position-derived.
	if s.Entities[0].Address == nil {
		t.Errorf("entity[0] must have explicit address")
	}
	if s.Entities[1].Address != nil {
		t.Errorf("entity[1] must rely on name-derivation (Address nil)")
	}
	if s.Entities[2].Address != nil || s.Entities[2].Name != "" {
		t.Errorf("entity[2] must rely on position-derivation (no address, no name)")
	}
}

func TestParseRejectsUnknownField(t *testing.T) {
	// `description:` is not a known field; KnownFields(true) must reject.
	input := `entities:
  - kind: eoa
    address: 0x1111111111111111111111111111111111111111
    description: "this should fail"
`
	if _, err := Parse(strings.NewReader(input)); err == nil {
		t.Fatal("expected error for unknown field, got nil")
	} else if !strings.Contains(err.Error(), "description") {
		t.Errorf("error should mention unknown field name: %v", err)
	}
}

func TestParseRejectsBadHexAddress(t *testing.T) {
	cases := []struct {
		name  string
		input string
	}{
		{"no-prefix", `entities:
  - kind: eoa
    address: "1111111111111111111111111111111111111111"
`},
		{"too-short", `entities:
  - kind: eoa
    address: 0x111
`},
		{"non-hex", `entities:
  - kind: eoa
    address: "0xZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZZ"
`},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := Parse(strings.NewReader(tc.input)); err == nil {
				t.Fatalf("expected error for %s, got nil", tc.name)
			}
		})
	}
}

func TestParseRejectsBadHexCode(t *testing.T) {
	input := `entities:
  - kind: contract
    name: bad
    code: "ABCDEF"   # missing 0x prefix
`
	if _, err := Parse(strings.NewReader(input)); err == nil {
		t.Fatal("expected error for non-prefixed hex code, got nil")
	}
}

func TestParseAcceptsQuotedBalance(t *testing.T) {
	input := `entities:
  - kind: eoa
    address: 0x1111111111111111111111111111111111111111
    balance: "1000000000000000000"
`
	s, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if s.Entities[0].Balance == nil {
		t.Fatal("balance not parsed")
	}
	if got := s.Entities[0].Balance.V.Uint64(); got != 1_000_000_000_000_000_000 {
		t.Errorf("balance: got %d, want 1e18", got)
	}
}

func TestParseAcceptsHexBalance(t *testing.T) {
	// 1 ETH = 0xde0b6b3a7640000 wei.
	input := `entities:
  - kind: eoa
    address: 0x1111111111111111111111111111111111111111
    balance: "0xde0b6b3a7640000"
`
	s, err := Parse(strings.NewReader(input))
	if err != nil {
		t.Fatalf("Parse: %v", err)
	}
	if got := s.Entities[0].Balance.V.Uint64(); got != 1_000_000_000_000_000_000 {
		t.Errorf("hex balance: got %d, want 1e18", got)
	}
}

func TestParseRejectsUntypedNumericBalance(t *testing.T) {
	// Unquoted balance — yaml.v3 parses as int64; for values > 2^53 this would
	// silently lose precision in JSON-compat YAML. The string-only decode
	// rejects it.
	input := `entities:
  - kind: eoa
    address: 0x1111111111111111111111111111111111111111
    balance: 1000000000000000000
`
	if _, err := Parse(strings.NewReader(input)); err == nil {
		t.Fatal("expected error for unquoted balance, got nil")
	}
}

func TestParseFileMissing(t *testing.T) {
	if _, err := ParseFile("testdata/this-file-does-not-exist.yaml"); err == nil {
		t.Fatal("expected error for missing file, got nil")
	}
}
