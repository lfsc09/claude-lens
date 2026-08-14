package status

import "sync/atomic"

// Fresh is a thread-safe counter the proxy bumps each time it saves a new
// exchange. The admin SSE stream compares Version() against the value it
// last pushed to tell "an exchange was saved" apart from "nothing changed
// since the last tick" without re-querying the database to find out.
//
// A plain atomic counter (rather than a mutex-guarded flag, as Flag uses)
// is deliberate: the SSE stream only ever reads Version and never consumes
// or resets it, so multiple concurrently open admin tabs each compare
// independently against their own last-seen value instead of racing to
// clear a shared flag.
type Fresh struct {
	version atomic.Uint64
}

// NewFresh creates a Fresh counter starting at 0.
func NewFresh() *Fresh {
	return &Fresh{}
}

// Bump records that a new exchange was saved.
func (f *Fresh) Bump() {
	f.version.Add(1)
}

// Version returns the current counter value. Two reads represent the same
// generation iff they're equal — the number itself carries no other
// meaning.
func (f *Fresh) Version() uint64 {
	return f.version.Load()
}
