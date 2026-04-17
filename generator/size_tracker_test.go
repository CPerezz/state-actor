package generator

import (
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"
	"testing"
)

// TestSizeTrackerEstimatedActualMonotone asserts that EstimatedActual
// never decreases across consecutive reads even when the compression
// ratio refreshes to a smaller value.
func TestSizeTrackerEstimatedActualMonotone(t *testing.T) {
	var logical atomic.Int64
	tmp := t.TempDir()
	// Write a file so dirSize is deterministic and > 0.
	if err := os.WriteFile(filepath.Join(tmp, "x"), make([]byte, 1000), 0o600); err != nil {
		t.Fatalf("seed file: %v", err)
	}
	tracker := newSizeTrackerWith(tmp, 10_000, logical.Load, []float64{0.5})

	logical.Store(2_000) // far below the 5_000 milestone; no calibration
	if got := tracker.EstimatedActual(); got != 2_000 {
		t.Errorf("pre-calibration estimate: got %d, want 2_000 (ratio=1.0)", got)
	}

	// Cross the milestone and observe the high-water mark before calibrating.
	logical.Store(6_000)
	preCalibHigh := tracker.EstimatedActual() // 6_000 (ratio still 1.0)
	if preCalibHigh != 6_000 {
		t.Fatalf("pre-calibration high-water: got %d, want 6_000", preCalibHigh)
	}

	// Calibrate: ratio now ≈ 1000/6000 ≈ 0.167. The raw new estimate would
	// be 1000, but the monotone clamp on reported values must keep it at
	// the previous high-water mark (6_000).
	if err := tracker.MaybeCalibrate(nil); err != nil {
		t.Fatalf("MaybeCalibrate: %v", err)
	}
	got := tracker.EstimatedActual()
	if got < preCalibHigh {
		t.Errorf("monotonicity violated: got %d, want >= %d", got, preCalibHigh)
	}
}

// TestSizeTrackerMilestonesFireOnceInOrder asserts each milestone
// triggers exactly once and in ascending order regardless of how many
// times MaybeCalibrate is called between threshold crossings.
func TestSizeTrackerMilestonesFireOnceInOrder(t *testing.T) {
	var logical atomic.Int64
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "x"), []byte("hello"), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tracker := newSizeTrackerWith(tmp, 1000, logical.Load, []float64{0.1, 0.5, 0.9})

	var calibrations int
	flushFn := func() error {
		calibrations++
		return nil
	}

	// Below first milestone (100): no calibration.
	logical.Store(50)
	_ = tracker.MaybeCalibrate(flushFn)
	_ = tracker.MaybeCalibrate(flushFn)
	if calibrations != 0 {
		t.Errorf("before first milestone: want 0 calibrations, got %d", calibrations)
	}

	// Cross first milestone (100): one calibration.
	logical.Store(150)
	_ = tracker.MaybeCalibrate(flushFn)
	_ = tracker.MaybeCalibrate(flushFn) // idempotent
	if calibrations != 1 {
		t.Errorf("after first milestone: want 1, got %d", calibrations)
	}

	// Cross second milestone (500): second calibration.
	logical.Store(600)
	_ = tracker.MaybeCalibrate(flushFn)
	if calibrations != 2 {
		t.Errorf("after second milestone: want 2, got %d", calibrations)
	}

	// Cross final milestone (900): third calibration, and nextIdx reaches end.
	logical.Store(1000)
	_ = tracker.MaybeCalibrate(flushFn)
	_ = tracker.MaybeCalibrate(flushFn) // past last milestone — should no-op
	if calibrations != 3 {
		t.Errorf("after last milestone: want 3, got %d", calibrations)
	}
	if tracker.NextMilestoneIdx() != 3 {
		t.Errorf("nextIdx: want 3, got %d", tracker.NextMilestoneIdx())
	}
}

// TestSizeTrackerShouldStop asserts ShouldStop fires exactly when the
// estimated actual size crosses the target.
func TestSizeTrackerShouldStop(t *testing.T) {
	var logical atomic.Int64
	tracker := NewSizeTracker(t.TempDir(), 10_000, logical.Load)

	logical.Store(5_000)
	if tracker.ShouldStop() {
		t.Error("ShouldStop fired below target")
	}
	logical.Store(10_000)
	if !tracker.ShouldStop() {
		t.Error("ShouldStop did not fire at exactly target")
	}
	logical.Store(20_000)
	if !tracker.ShouldStop() {
		t.Error("ShouldStop stopped firing above target")
	}
}

// TestSizeTrackerZeroTarget asserts that a zero target disables the
// stop entirely and makes calibration a no-op.
func TestSizeTrackerZeroTarget(t *testing.T) {
	var logical atomic.Int64
	tracker := NewSizeTracker(t.TempDir(), 0, logical.Load)

	logical.Store(1_000_000)
	if tracker.ShouldStop() {
		t.Error("ShouldStop fired with target=0")
	}
	calibrated := false
	_ = tracker.MaybeCalibrate(func() error { calibrated = true; return nil })
	if calibrated {
		t.Error("MaybeCalibrate triggered with target=0")
	}
}

// TestSizeTrackerConcurrentReaders verifies that concurrent readers
// racing with a single calibrator goroutine produce no data races
// (run with -race) and that monotonicity holds under load.
func TestSizeTrackerConcurrentReaders(t *testing.T) {
	var logical atomic.Int64
	tmp := t.TempDir()
	if err := os.WriteFile(filepath.Join(tmp, "x"), make([]byte, 100), 0o600); err != nil {
		t.Fatalf("seed: %v", err)
	}
	tracker := newSizeTrackerWith(tmp, 10_000, logical.Load, []float64{0.1, 0.3, 0.5, 0.7, 0.9})

	var wg sync.WaitGroup
	for i := 0; i < 8; i++ {
		wg.Add(1)
		go func() {
			defer wg.Done()
			var last uint64
			for j := 0; j < 5000; j++ {
				cur := tracker.EstimatedActual()
				if cur < last {
					t.Errorf("monotonicity violated: %d < %d", cur, last)
					return
				}
				last = cur
				tracker.ShouldStop()
			}
		}()
	}

	wg.Add(1)
	go func() {
		defer wg.Done()
		for step := int64(500); step <= 10_000; step += 500 {
			logical.Store(step)
			_ = tracker.MaybeCalibrate(nil)
		}
	}()

	wg.Wait()
}

// TestStoppableIteratorFalseFromPredicate ensures the wrapper cleanly
// reports no-more-elements without touching the underlying iterator
// once the stop predicate fires.
func TestStoppableIteratorFalseFromPredicate(t *testing.T) {
	inner := &fakeIter{remaining: 10}
	var stopAfter atomic.Int32
	stopAfter.Store(3)
	s := &stoppableIterator{
		Iterator:   inner,
		shouldStop: func() bool { return inner.consumed >= int(stopAfter.Load()) },
	}

	seen := 0
	for s.Next() {
		seen++
		if seen > 100 {
			t.Fatal("runaway iteration")
		}
	}
	if seen != 3 {
		t.Errorf("expected 3 Next() hits before stop, got %d", seen)
	}
	if inner.remaining == 0 {
		t.Error("iterator should not have been exhausted")
	}
	// Subsequent calls stay false.
	if s.Next() {
		t.Error("Next() after stop returned true")
	}
}

// fakeIter is a minimal ethdb.Iterator stand-in for the wrapper test.
type fakeIter struct {
	remaining int
	consumed  int
}

func (f *fakeIter) Next() bool {
	if f.remaining == 0 {
		return false
	}
	f.remaining--
	f.consumed++
	return true
}
func (f *fakeIter) Error() error   { return nil }
func (f *fakeIter) Key() []byte    { return nil }
func (f *fakeIter) Value() []byte  { return nil }
func (f *fakeIter) Release()       {}
