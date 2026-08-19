// Package status holds the proxy's health as an in-process flag shared
// between the proxy and admin servers, so the admin server can read the
// proxy's health with a plain mutex-guarded read instead of a network call.
package status

import "sync"

// Value is the health of the proxy as observed by its own forwarding
// activity.
type Value string

const (
	// OK means the proxy is serving and its most recent forward to
	// upstream succeeded (or no forward has happened yet since startup).
	OK Value = "ok"
	// Degraded means the proxy is serving but its most recent forward to
	// upstream failed (connection error, timeout, etc.).
	Degraded Value = "degraded"
	// Unreachable means the proxy server is not currently listening
	// (not yet started, or shut down).
	Unreachable Value = "unreachable"
)

// Flag is a thread-safe holder for the proxy's current health Value.
type Flag struct {
	mu    sync.RWMutex
	value Value
}

// New creates a Flag starting in the Unreachable state — accurate until the
// proxy server's Run reports it has started listening.
func New() *Flag {
	return &Flag{value: Unreachable}
}

// Set updates the current health value.
func (f *Flag) Set(v Value) {
	f.mu.Lock()
	f.value = v
	f.mu.Unlock()
}

// Get returns the current health value.
func (f *Flag) Get() Value {
	f.mu.RLock()
	defer f.mu.RUnlock()
	return f.value
}
