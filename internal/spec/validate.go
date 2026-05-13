package spec

import (
	"fmt"
	"strings"
)

// approxSizeWarnThreshold is the upper bound on approximate_size_bytes above
// which Validate emits a warning (returned in the error.Warnings field of the
// returned ValidateResult — not a hard failure). 1 TB per entity is far
// outside any reasonable single-contract workload; if a user requests more,
// they probably mean MB not TB.
const approxSizeWarnThreshold uint64 = 1 << 40 // 1 TiB

// MaxCodeSize is EIP-170's contract-code ceiling (24,576 bytes). Any
// `code:` field longer than this is rejected at parse time — a contract
// with oversize code would be rejected by clients at execution time
// anyway, and behavior at genesis may diverge per client (some refuse
// to load the genesis, others accept it silently). Better to fail
// loud here.
const MaxCodeSize = 24576

// ValidateResult carries the validator's diagnostics. Validate returns a
// non-nil error if any hard rule is violated; warnings (non-fatal) are
// surfaced in Warnings so the CLI can print them but still proceed.
type ValidateResult struct {
	Warnings []string
}

// Validate applies the v1 schema rules to s. knownTemplates is the set of
// template names the templates package has registered; an unknown template
// reference fails validation. Pass an empty slice to disable the registered-
// template check (useful in tests that don't exercise the templates path).
//
// Rules enforced (matches the v1 schema documented in docs/SPEC.md):
//   - kind ∈ {contract, eoa}.
//   - kind=contract: exactly one of `template` or `code` must be set.
//   - kind=eoa: must NOT set `template` or `parameters`; may set `code`.
//   - `parameters` only valid alongside `template`.
//   - No duplicate explicit `address` across entities (case-insensitive).
//     Collisions involving derived (name/position) addresses are checked
//     later in internal/specbuild/.
//   - `template` value must appear in knownTemplates (when that slice is
//     non-empty).
//   - `approximate_size_bytes`: 0 is fine (means "no extra storage"); values
//     above 1 TiB emit a warning.
func (s *Spec) Validate(knownTemplates []string) (ValidateResult, error) {
	result := ValidateResult{}

	knownTemplateSet := make(map[string]struct{}, len(knownTemplates))
	for _, t := range knownTemplates {
		knownTemplateSet[t] = struct{}{}
	}

	if len(s.Entities) == 0 {
		return result, fmt.Errorf("spec has no entities; at least one is required")
	}

	seenAddrs := make(map[string]int) // lowercased address hex → first index it appeared at

	for i, e := range s.Entities {
		// Anchor every diagnostic to the entity's index in the YAML so users
		// can find the offending block in their file. We include the name in
		// parens when set because that's often more informative than "entity 4".
		anchor := fmt.Sprintf("entities[%d]", i)
		if e.Name != "" {
			anchor = fmt.Sprintf("entities[%d] (name=%q)", i, e.Name)
		}

		switch e.Kind {
		case KindContract:
			hasTemplate := e.Template != ""
			hasCode := len(e.Code) > 0
			if !hasTemplate && !hasCode {
				return result, fmt.Errorf("%s: kind=contract requires either `template` or `code`", anchor)
			}
			if hasTemplate && hasCode {
				return result, fmt.Errorf("%s: kind=contract must NOT set both `template` and `code`", anchor)
			}
			if hasTemplate && len(knownTemplateSet) > 0 {
				if _, ok := knownTemplateSet[e.Template]; !ok {
					return result, fmt.Errorf("%s: unknown template %q (known: %v)", anchor, e.Template, knownTemplates)
				}
			}
			if !hasTemplate && len(e.Parameters) > 0 {
				return result, fmt.Errorf("%s: `parameters` is only valid with `template`", anchor)
			}
		case KindEOA:
			if e.Template != "" {
				return result, fmt.Errorf("%s: kind=eoa must NOT set `template`", anchor)
			}
			if len(e.Parameters) > 0 {
				return result, fmt.Errorf("%s: kind=eoa must NOT set `parameters`", anchor)
			}
			// code/storage/balance/nonce all allowed for EOAs.
		case "":
			return result, fmt.Errorf("%s: missing required field `kind`", anchor)
		default:
			return result, fmt.Errorf("%s: unknown kind %q (expected %q or %q)", anchor, e.Kind, KindContract, KindEOA)
		}

		// Explicit-address duplicate detection. Derived-address collisions are
		// caught later in specbuild.Build once addresses are concrete.
		if e.Address != nil {
			key := strings.ToLower(e.Address.Address().Hex())
			if prev, dup := seenAddrs[key]; dup {
				return result, fmt.Errorf("%s: duplicate address %s (first appeared at entities[%d])", anchor, key, prev)
			}
			seenAddrs[key] = i
		}

		// Size warnings — non-fatal.
		if e.ApproximateSizeBytes > approxSizeWarnThreshold {
			result.Warnings = append(result.Warnings,
				fmt.Sprintf("%s: approximate_size_bytes=%d exceeds %d (1 TiB); likely a unit mistake",
					anchor, e.ApproximateSizeBytes, approxSizeWarnThreshold))
		}

		// EIP-170 code-size check. Genesis state can technically contain
		// oversize code on some testnets, but mainnet rules reject it and
		// per-client behavior diverges; fail loud at spec time.
		if len(e.Code) > MaxCodeSize {
			return result, fmt.Errorf("%s: code length %d exceeds EIP-170 limit (%d bytes); contracts with oversize code are unmineable on mainnet-rules forks",
				anchor, len(e.Code), MaxCodeSize)
		}
	}

	return result, nil
}
