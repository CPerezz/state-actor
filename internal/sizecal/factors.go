package sizecal

import (
	_ "embed"
	"encoding/json"
	"fmt"
)

// SizeApproximator translates a target on-disk byte budget into a synthetic
// storage-slot count. Implementations are looked up by client name.
//
// The interface is duplicated (in shape) in internal/templates/template.go
// to avoid an import cycle — templates needs the interface to plumb through
// Template.Expand; sizecal owns the implementation. Both interfaces are
// trivially compatible by signature.
type SizeApproximator interface {
	SlotsForBytes(client string, targetBytes uint64) int
}

// factors is the package-level table loaded from factors.json at init.
// Unknown clients return the fallback ratio.
var factors map[string]uint64

//go:embed factors.json
var factorsJSON []byte

// fallbackBytesPerSlot is used when a client name isn't in factors.json.
// Conservative — picks the larger end of observed ratios so an unknown
// client over-allocates (better than under-allocating and busting the
// target-size budget).
const fallbackBytesPerSlot uint64 = 100

func init() {
	var raw map[string]any
	if err := json.Unmarshal(factorsJSON, &raw); err != nil {
		panic(fmt.Sprintf("sizecal: failed to decode embedded factors.json: %v", err))
	}
	factors = make(map[string]uint64, len(raw))
	for k, v := range raw {
		// Skip JSON _comment keys and any non-numeric entries.
		switch n := v.(type) {
		case float64:
			factors[k] = uint64(n)
		case int:
			factors[k] = uint64(n)
		}
	}
}

// Default returns the package-level SizeApproximator backed by the embedded
// factors.json. Callers should generally use this; tests that need a
// specific factor can use NewFixed instead.
func Default() SizeApproximator {
	return &defaultSizer{}
}

// NewFixed returns a SizeApproximator that always uses the given bytes-per-
// slot ratio, regardless of client. Useful in tests and as a fallback when
// a user supplies their own --bytes-per-slot override (a future CLI knob).
func NewFixed(bytesPerSlot uint64) SizeApproximator {
	return &fixedSizer{bytesPerSlot: bytesPerSlot}
}

// BytesPerSlot is the calibrated ratio for a client. Returns the fallback
// when the client isn't in the table. Exported so tests + diagnostics can
// inspect the factor without going through SlotsForBytes math.
func BytesPerSlot(client string) uint64 {
	if v, ok := factors[client]; ok {
		return v
	}
	return fallbackBytesPerSlot
}

type defaultSizer struct{}

func (defaultSizer) SlotsForBytes(client string, targetBytes uint64) int {
	bps := BytesPerSlot(client)
	if bps == 0 {
		return 0
	}
	return int(targetBytes / bps)
}

type fixedSizer struct{ bytesPerSlot uint64 }

func (s fixedSizer) SlotsForBytes(client string, targetBytes uint64) int {
	if s.bytesPerSlot == 0 {
		return 0
	}
	return int(targetBytes / s.bytesPerSlot)
}
