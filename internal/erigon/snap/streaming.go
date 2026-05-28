package snap

import (
	"github.com/nerolation/state-actor/internal/streamsort"
)

// FromStreamsort adapts a *streamsort.Store into the push-style entry
// iterator that Writer.WriteDomain expects. The streamsort spills to a
// temp Pebble LSM that auto-sorts by key, so iteration order is
// guaranteed ascending — which is exactly the contract WriteDomain
// requires.
//
// The returned iterator copies key/value bytes into the DomainEntry
// passed to yield. Pebble aliases its internal buffers across Next()
// calls (see streamsort.Iterate doc), but WriteDomain consumes each
// DomainEntry synchronously (immediately AddWord-s key + value into the
// seg compressor) before calling yield again — so aliasing is safe
// here. We deliberately do NOT copy to avoid the allocation per row at
// 100M+ entry scale.
//
// Errors from streamsort.Iterate are not surfaced to the yield consumer
// (the push-style signature has no error channel); they are dropped on
// the floor. Callers concerned with iterator errors should wrap with a
// helper that captures them out-of-band — but in practice the only
// failure mode is "Store already Closed", which the caller controls.
func FromStreamsort(s *streamsort.Store) func(yield func(DomainEntry) bool) {
	return func(yield func(DomainEntry) bool) {
		_ = s.Iterate(func(key, value []byte) error {
			if !yield(DomainEntry{Key: key, Value: value}) {
				return errStreamingStop
			}
			return nil
		})
	}
}

// FromStreamsortRange adapts a *streamsort.Store containing
// COMPOSITE keys (rangeIdx:u8 || originalKey) into the same push-style
// iterator, but yielding ONLY entries whose composite-key starts with
// the byte rangeIdx, with that byte stripped before being passed to
// WriteDomain.
//
// Used by the multi-range orchestrator (snapshot_cgo.go) — one
// streamsort per domain holds all 5 ranges' data; this adapter
// prefix-scans one range at a time. Per agent C: 1 store per domain
// (4 total) is preferable to 5×4=20 stores because each Pebble
// instance has nontrivial memtable/cache/WAL overhead.
//
// Iteration order within the range is ascending by originalKey
// (Pebble's bytewise comparator on the composite key implies
// originalKey order within a single rangeIdx prefix).
//
// The yielded DomainEntry slices alias Pebble's internal buffers
// (same caveat as FromStreamsort). The yielded Key is a slice INTO
// the underlying composite-key buffer with the rangeIdx byte
// trimmed via re-slicing — no copy.
func FromStreamsortRange(s *streamsort.Store, rangeIdx uint8) func(yield func(DomainEntry) bool) {
	return func(yield func(DomainEntry) bool) {
		_ = s.Iterate(func(key, value []byte) error {
			if len(key) == 0 {
				return nil // malformed composite key — skip
			}
			if key[0] < rangeIdx {
				return nil // not our range yet
			}
			if key[0] > rangeIdx {
				return errStreamingStop // past our range — short-circuit
			}
			if !yield(DomainEntry{Key: key[1:], Value: value}) {
				return errStreamingStop
			}
			return nil
		})
	}
}

// errStreamingStop is the sentinel returned to streamsort.Iterate when
// the WriteDomain consumer signals stop via yield→false. streamsort
// short-circuits on any non-nil error, so this cleanly exits the inner
// scan without surfacing as a "real" error to the caller.
var errStreamingStop = streamingStopError{}

type streamingStopError struct{}

func (streamingStopError) Error() string { return "snap.FromStreamsort: consumer requested stop" }
