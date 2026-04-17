package generator

import (
	"math"
	"sync/atomic"
)

// SizeTracker projects the current on-disk main-DB size from
// atomic logical-byte counters plus a periodically-calibrated Pebble
// compression ratio.
//
// Design:
//   - `logicalFn` returns the current sum of atomic byte counters from
//     writers that contribute to the main DB (code blobs, stem blobs,
//     trie nodes, etc.). It is called from any goroutine and must be
//     cheap (a handful of atomic.Load calls).
//   - The compression ratio (`actual_disk_bytes / logical_bytes`) is
//     refreshed at deterministic logical-byte milestones via
//     MaybeCalibrate, which MUST be called by a goroutine that owns
//     the Pebble batches being flushed — Pebble batches are not
//     concurrent-safe. The initial ratio is 1.0, which overestimates
//     actual bytes (Pebble never expands on net) — a conservative
//     default that never causes premature stop.
//   - EstimatedActual is lock-free and monotone non-decreasing; the
//     monotonicity clamp prevents UI progress bars from snapping
//     backwards when a fresh calibration reveals a smaller ratio.
//
// Determinism: milestones are indexed by `logical >= pct × target`,
// which is deterministic given the same seed + config. No wall-clock
// inputs, so same-seed runs stop at the same contract index and
// produce the same state root.
type SizeTracker struct {
	dbPath     string
	logicalFn  func() int64
	target     uint64
	milestones []float64

	nextIdx    atomic.Int32  // index of next milestone to trigger
	ratio      atomic.Uint64 // math.Float64bits of compression ratio
	lastReport atomic.Uint64 // monotone max of EstimatedActual
}

// DefaultCalibrationMilestones are the logical-byte fractions of the
// target at which the tracker triggers a forced flush + dirSize to
// refresh the compression ratio. Weighted toward the late phase where
// accuracy matters most and the ratio has stabilized.
var DefaultCalibrationMilestones = []float64{0.10, 0.30, 0.60, 0.80, 0.90, 0.95, 0.98, 0.99}

// NewSizeTracker builds a tracker with the default milestone schedule.
// target==0 means "no target" — EstimatedActual still works, ShouldStop
// always returns false, MaybeCalibrate is a no-op.
func NewSizeTracker(dbPath string, target uint64, logicalFn func() int64) *SizeTracker {
	return newSizeTrackerWith(dbPath, target, logicalFn, DefaultCalibrationMilestones)
}

// newSizeTrackerWith is a test-only constructor allowing custom milestone schedules.
func newSizeTrackerWith(dbPath string, target uint64, logicalFn func() int64, milestones []float64) *SizeTracker {
	t := &SizeTracker{
		dbPath:     dbPath,
		logicalFn:  logicalFn,
		target:     target,
		milestones: milestones,
	}
	t.ratio.Store(math.Float64bits(1.0))
	return t
}

// EstimatedActual returns the best estimate of the current on-disk
// main-DB size. Monotone non-decreasing across consecutive calls.
// Safe for concurrent readers.
func (t *SizeTracker) EstimatedActual() uint64 {
	l := t.logicalFn()
	if l < 0 {
		l = 0
	}
	r := math.Float64frombits(t.ratio.Load())
	est := uint64(float64(l) * r)
	for {
		prev := t.lastReport.Load()
		if est <= prev {
			return prev
		}
		if t.lastReport.CompareAndSwap(prev, est) {
			return est
		}
	}
}

// ShouldStop returns true when the estimated actual size has reached
// the configured target. Returns false if target is zero (no limit).
func (t *SizeTracker) ShouldStop() bool {
	if t.target == 0 {
		return false
	}
	return t.EstimatedActual() >= t.target
}

// MaybeCalibrate triggers a forced flush + dirSize when logical bytes
// have crossed the next milestone. Must be called by a goroutine that
// owns the Pebble batches being flushed — flushFn is expected to commit
// the caller's own batches synchronously. No-op if target is zero, if
// all milestones are consumed, or if the next milestone hasn't been
// reached. Idempotent — safe to call every iteration.
func (t *SizeTracker) MaybeCalibrate(flushFn func() error) error {
	if t.target == 0 {
		return nil
	}
	logical := t.logicalFn()
	if logical < 0 {
		return nil
	}
	idx := t.nextIdx.Load()
	if int(idx) >= len(t.milestones) {
		return nil
	}
	threshold := uint64(t.milestones[idx] * float64(t.target))
	if uint64(logical) < threshold {
		return nil
	}
	if !t.nextIdx.CompareAndSwap(idx, idx+1) {
		return nil // another goroutine raced us to this milestone
	}
	if flushFn != nil {
		if err := flushFn(); err != nil {
			return err
		}
	}
	disk, err := dirSize(t.dbPath)
	if err != nil {
		return err
	}
	if logical == 0 {
		return nil
	}
	r := float64(disk) / float64(logical)
	t.ratio.Store(math.Float64bits(r))
	return nil
}

// Ratio returns the current compression ratio. Exposed for logging/tests.
func (t *SizeTracker) Ratio() float64 {
	return math.Float64frombits(t.ratio.Load())
}

// NextMilestoneIdx returns the index of the next unconsumed milestone.
// Exposed for tests.
func (t *SizeTracker) NextMilestoneIdx() int {
	return int(t.nextIdx.Load())
}
