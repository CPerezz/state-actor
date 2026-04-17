package generator

import "github.com/ethereum/go-ethereum/ethdb"

// stoppableIterator wraps an ethdb.Iterator with an external stop
// predicate. When shouldStop() returns true, Next() returns false
// cleanly (without touching the wrapped iterator), which mirrors
// natural exhaustion. The caller's downstream pipeline therefore
// drains and finalizes normally — no context cancellation, no Error().
//
// This is the supported shape for early termination of
// computeBinaryRootStreamingParallel and MPT Phase-2 iteration: the
// reader loop exits its for-loop, the partial stem/account is flushed,
// and the builder produces a valid state root for the processed subset.
type stoppableIterator struct {
	ethdb.Iterator
	shouldStop func() bool
	stopped    bool
}

// Next returns false as soon as the predicate fires. Once stopped,
// subsequent calls stay false even if the predicate flips — iterators
// are one-shot.
func (s *stoppableIterator) Next() bool {
	if s.stopped {
		return false
	}
	if s.shouldStop != nil && s.shouldStop() {
		s.stopped = true
		return false
	}
	return s.Iterator.Next()
}
