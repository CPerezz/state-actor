package spec

import (
	"encoding/hex"
	"fmt"
	"strings"

	"github.com/ethereum/go-ethereum/common"
	"github.com/holiman/uint256"
	"gopkg.in/yaml.v3"
)

// Spec is the top-level YAML document.
type Spec struct {
	Entities []Entity `yaml:"entities"`
}

// Kind discriminates entity types. Stringly-typed at the YAML layer to keep
// schema diagnostics readable ("unknown kind: contracst" beats "value out of
// range").
const (
	KindContract = "contract"
	KindEOA      = "eoa"
)

// Entity is one declared entity in the spec. A single struct discriminated by
// Kind keeps the YAML decode straight-line; per-kind field rules (e.g. "eoa
// must not set template") are enforced by Validate, not by separate Go types.
type Entity struct {
	Kind                 string         `yaml:"kind"`
	Name                 string         `yaml:"name,omitempty"`
	Address              *HexAddress    `yaml:"address,omitempty"`
	Balance              *BigIntDecimal `yaml:"balance,omitempty"`
	Nonce                uint64         `yaml:"nonce,omitempty"`
	Code                 HexBytes       `yaml:"code,omitempty"`
	Template             string         `yaml:"template,omitempty"`
	Parameters           map[string]any `yaml:"parameters,omitempty"`
	ApproximateSizeBytes uint64         `yaml:"approximate_size_bytes,omitempty"`
}

// scalarText returns the raw source text of a YAML scalar node. yaml.v3 may
// resolve `0x...` syntax as an integer (`!!int`) rather than a string — but
// node.Value preserves the verbatim text either way, so for hex-prefixed
// fields the type tag doesn't matter; we validate against the raw text.
func scalarText(node *yaml.Node, fieldDesc string) (string, error) {
	if node.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s must be a scalar (got node kind %d at line %d)", fieldDesc, node.Kind, node.Line)
	}
	return strings.TrimSpace(node.Value), nil
}

// requireStringTag asserts that a YAML scalar was written as a quoted string,
// not an unquoted integer/float/boolean. Used for fields where the schema
// requires user intent to be explicit — primarily `balance`, where unquoted
// large numbers risk silent precision loss via int64/float64 coercion.
func requireStringTag(node *yaml.Node, fieldDesc string) error {
	if node.Kind != yaml.ScalarNode {
		return fmt.Errorf("%s must be a scalar (got node kind %d at line %d)", fieldDesc, node.Kind, node.Line)
	}
	if tag := node.ShortTag(); tag != "!!str" {
		return fmt.Errorf("%s must be a quoted string (got %s scalar %q at line %d); add quotes",
			fieldDesc, tag, node.Value, node.Line)
	}
	return nil
}

// HexBytes is `[]byte` decoded from a `0x`-prefixed hex scalar in YAML.
// Empty strings decode to nil. Both quoted (`code: "0x6080"`) and unquoted
// (`code: 0x6080`) forms are accepted — yaml.v3 resolves the latter as an
// integer, but node.Value preserves the source text.
type HexBytes []byte

func (b *HexBytes) UnmarshalYAML(node *yaml.Node) error {
	s, err := scalarText(node, "hex bytes")
	if err != nil {
		return err
	}
	if s == "" {
		*b = nil
		return nil
	}
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return fmt.Errorf("hex bytes must have 0x prefix, got %q (line %d)", s, node.Line)
	}
	out, err := hex.DecodeString(s[2:])
	if err != nil {
		return fmt.Errorf("decode hex bytes %q (line %d): %w", s, node.Line, err)
	}
	*b = out
	return nil
}

// HexAddress is `common.Address` decoded from a `0x...` 20-byte hex scalar.
// The 0x prefix is required; checksum case is not enforced. Both quoted and
// unquoted `0x` forms are accepted.
type HexAddress common.Address

func (a *HexAddress) UnmarshalYAML(node *yaml.Node) error {
	s, err := scalarText(node, "address")
	if err != nil {
		return err
	}
	if !strings.HasPrefix(s, "0x") && !strings.HasPrefix(s, "0X") {
		return fmt.Errorf("address must have 0x prefix, got %q (line %d)", s, node.Line)
	}
	if len(s) != 2+2*common.AddressLength {
		return fmt.Errorf("address must be 20 bytes (42 hex chars including 0x), got %d chars in %q (line %d)",
			len(s), s, node.Line)
	}
	raw, err := hex.DecodeString(s[2:])
	if err != nil {
		return fmt.Errorf("decode address %q (line %d): %w", s, node.Line, err)
	}
	copy(a[:], raw)
	return nil
}

// Address returns the common.Address representation.
func (a HexAddress) Address() common.Address { return common.Address(a) }

// BigIntDecimal wraps a *uint256.Int with a string-only YAML decode. Untyped
// numeric YAML scalars are rejected because `1e22` would decode as a float64
// and lose precision past 2^53; large balances overflow int64 entirely. The
// schema requires balances to be quoted strings.
type BigIntDecimal struct {
	V *uint256.Int
}

func (b *BigIntDecimal) UnmarshalYAML(node *yaml.Node) error {
	if err := requireStringTag(node, "balance"); err != nil {
		return err
	}
	s := strings.TrimSpace(node.Value)
	if s == "" {
		return fmt.Errorf("balance is empty (line %d)", node.Line)
	}
	v, err := parseUint256(s)
	if err != nil {
		return fmt.Errorf("decode balance %q (line %d): %w", s, node.Line, err)
	}
	b.V = v
	return nil
}

// parseUint256 accepts decimal (e.g. "1000000000000000000") or 0x-hex
// (e.g. "0xde0b6b3a7640000") representations and returns a *uint256.Int.
// Underscores are not accepted (YAML strings don't strip them).
func parseUint256(s string) (*uint256.Int, error) {
	v := new(uint256.Int)
	if strings.HasPrefix(s, "0x") || strings.HasPrefix(s, "0X") {
		if err := v.SetFromHex(s); err != nil {
			return nil, err
		}
		return v, nil
	}
	if err := v.SetFromDecimal(s); err != nil {
		return nil, err
	}
	return v, nil
}
