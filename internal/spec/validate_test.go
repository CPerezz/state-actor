package spec

import (
	"strings"
	"testing"
)

// parseStr is a test-only helper that lets each rule test live in a single
// table entry instead of bouncing through testdata files.
func parseStr(t *testing.T, src string) *Spec {
	t.Helper()
	s, err := Parse(strings.NewReader(src))
	if err != nil {
		t.Fatalf("parse: %v", err)
	}
	return s
}

func TestValidateAcceptsStory1(t *testing.T) {
	s, err := ParseFile("testdata/valid-story1.yaml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if _, err := s.Validate([]string{"erc20"}); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestValidateAcceptsStory2(t *testing.T) {
	s, err := ParseFile("testdata/valid-story2.yaml")
	if err != nil {
		t.Fatalf("ParseFile: %v", err)
	}
	if _, err := s.Validate([]string{"erc20"}); err != nil {
		t.Errorf("Validate: %v", err)
	}
}

func TestValidateRejectsEmptyEntities(t *testing.T) {
	s := &Spec{}
	if _, err := s.Validate(nil); err == nil {
		t.Fatal("expected error for empty entities, got nil")
	}
}

func TestValidateContractRules(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		errFrag string // substring expected in error message
	}{
		{
			name: "no-template-no-code",
			yaml: `entities:
  - kind: contract
    name: empty
`,
			errFrag: "requires either `template` or `code`",
		},
		{
			name: "both-template-and-code",
			yaml: `entities:
  - kind: contract
    name: clash
    template: erc20
    code: "0x6080"
`,
			errFrag: "must NOT set both",
		},
		{
			name: "parameters-without-template",
			yaml: `entities:
  - kind: contract
    name: paramless
    code: "0x6080"
    parameters:
      foo: bar
`,
			errFrag: "`parameters` is only valid with `template`",
		},
		{
			name: "unknown-template",
			yaml: `entities:
  - kind: contract
    name: surprise
    template: erc999
`,
			errFrag: "unknown template",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := parseStr(t, tc.yaml)
			_, err := s.Validate([]string{"erc20"})
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.errFrag) {
				t.Errorf("expected error to contain %q, got %q", tc.errFrag, err.Error())
			}
		})
	}
}

func TestValidateEOARules(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		errFrag string
	}{
		{
			name: "eoa-with-template",
			yaml: `entities:
  - kind: eoa
    address: 0x1111111111111111111111111111111111111111
    template: erc20
`,
			errFrag: "kind=eoa must NOT set `template`",
		},
		{
			name: "eoa-with-parameters",
			yaml: `entities:
  - kind: eoa
    address: 0x1111111111111111111111111111111111111111
    parameters:
      foo: bar
`,
			errFrag: "kind=eoa must NOT set `parameters`",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := parseStr(t, tc.yaml)
			_, err := s.Validate([]string{"erc20"})
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.errFrag) {
				t.Errorf("expected error to contain %q, got %q", tc.errFrag, err.Error())
			}
		})
	}
}

func TestValidateKindRules(t *testing.T) {
	cases := []struct {
		name    string
		yaml    string
		errFrag string
	}{
		{
			name: "missing-kind",
			yaml: `entities:
  - name: no-kind
    template: erc20
`,
			errFrag: "missing required field `kind`",
		},
		{
			name: "unknown-kind",
			yaml: `entities:
  - kind: contracst
    template: erc20
`,
			errFrag: "unknown kind",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			s := parseStr(t, tc.yaml)
			_, err := s.Validate([]string{"erc20"})
			if err == nil {
				t.Fatal("expected validation error, got nil")
			}
			if !strings.Contains(err.Error(), tc.errFrag) {
				t.Errorf("expected error to contain %q, got %q", tc.errFrag, err.Error())
			}
		})
	}
}

func TestValidateDuplicateAddress(t *testing.T) {
	yaml := `entities:
  - kind: eoa
    address: 0x1111111111111111111111111111111111111111
  - kind: contract
    template: erc20
    address: 0x1111111111111111111111111111111111111111
`
	s := parseStr(t, yaml)
	_, err := s.Validate([]string{"erc20"})
	if err == nil {
		t.Fatal("expected duplicate-address error, got nil")
	}
	if !strings.Contains(err.Error(), "duplicate address") {
		t.Errorf("expected error to mention duplicate, got %q", err.Error())
	}
}

func TestValidateDuplicateAddressCaseInsensitive(t *testing.T) {
	// Same address, different case → duplicate.
	yaml := `entities:
  - kind: eoa
    address: 0xAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAAA
  - kind: eoa
    address: 0xaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaaa
`
	s := parseStr(t, yaml)
	_, err := s.Validate(nil)
	if err == nil {
		t.Fatal("expected case-insensitive duplicate detection, got nil")
	}
}

func TestValidateApproximateSizeWarning(t *testing.T) {
	// 2 TiB — above the 1 TiB warning threshold.
	yaml := `entities:
  - kind: contract
    template: erc20
    name: huge
    approximate_size_bytes: 2199023255552
`
	s := parseStr(t, yaml)
	res, err := s.Validate([]string{"erc20"})
	if err != nil {
		t.Fatalf("Validate: %v", err)
	}
	if len(res.Warnings) == 0 {
		t.Error("expected size warning, got none")
	}
}

func TestValidateEmptyKnownTemplatesSkipsCheck(t *testing.T) {
	// When the caller passes an empty knownTemplates slice, the registry
	// check is bypassed (useful for parse-only callers / tests that don't
	// load the templates package).
	yaml := `entities:
  - kind: contract
    template: anything-goes
`
	s := parseStr(t, yaml)
	if _, err := s.Validate(nil); err != nil {
		t.Errorf("expected pass with empty knownTemplates, got %v", err)
	}
}
