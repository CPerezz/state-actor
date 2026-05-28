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

// errStreamingStop is the sentinel returned to streamsort.Iterate when
// the WriteDomain consumer signals stop via yield→false. streamsort
// short-circuits on any non-nil error, so this cleanly exits the inner
// scan without surfacing as a "real" error to the caller.
var errStreamingStop = streamingStopError{}

type streamingStopError struct{}

func (streamingStopError) Error() string { return "snap.FromStreamsort: consumer requested stop" }
