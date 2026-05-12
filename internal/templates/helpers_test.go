package templates

// fixedSizer is a SizeApproximator stub for tests. The real per-client
// calibration lives in internal/sizecal/; templates should never depend on
// that package, so tests use this fixed-ratio impl.
type fixedSizer struct {
	bytesPerSlot uint64
}

func (s fixedSizer) SlotsForBytes(client string, targetBytes uint64) int {
	if s.bytesPerSlot == 0 {
		return 0
	}
	return int(targetBytes / s.bytesPerSlot)
}
