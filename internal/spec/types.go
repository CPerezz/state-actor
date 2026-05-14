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

const (
	KindContract = "contract"
	KindEOA      = "eoa"
)

// Entity is one declared entity in the spec; Kind discriminates.
// Per-kind field rules (e.g. "eoa must not set template") are enforced
// by Validate.
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

// scalarText returns the raw source text of a YAML scalar. Hex-prefixed
// fields validate against this rather than the tag because yaml.v3 may
// resolve `0x...` as an integer.
func scalarText(node *yaml.Node, fieldDesc string) (string, error) {
	if node.Kind != yaml.ScalarNode {
		return "", fmt.Errorf("%s must be a scalar (got node kind %d at line %d)", fieldDesc, node.Kind, node.Line)
	}
	return strings.TrimSpace(node.Value), nil
}

// requireStringTag rejects unquoted scalars. Used for fields like
// `balance` where unquoted large numbers risk int64/float64 precision
// loss.
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

// HexBytes is []byte decoded from a 0x-prefixed hex scalar. Empty
// strings decode to nil. Both quoted and unquoted 0x forms work.
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

// HexAddress is common.Address decoded from a 0x-prefixed 20-byte hex
// scalar. Checksum case is not enforced.
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

func (a HexAddress) Address() common.Address { return common.Address(a) }

// BigIntDecimal is a *uint256.Int decoded from a quoted YAML string.
// Unquoted numerics are rejected because 1e22 would lose precision via
// float64 and large balances overflow int64.
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
	v, err := ParseUint256(s)
	if err != nil {
		return fmt.Errorf("decode balance %q (line %d): %w", s, node.Line, err)
	}
	b.V = v
	return nil
}

// ParseUint256 accepts a decimal or 0x-hex string and returns a
// *uint256.Int. Underscores are not accepted.
func ParseUint256(s string) (*uint256.Int, error) {
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
