//go:build linux

package sandbox

// backendsForPlatform returns the backends to probe, in preference order.
// bubblewrap is preferred because it can subtract protected paths from a
// writable root; Landlock is the fallback.
func backendsForPlatform() []Backend {
	// bubblewrap first (it can subtract protected paths), then Landlock, then
	// the in-process fence as a last resort so the file tools and the
	// interpreter are always fenced even with no kernel backend.
	return []Backend{&bubblewrapBackend{}, &landlockBackend{}, fenceBackend{}}
}
