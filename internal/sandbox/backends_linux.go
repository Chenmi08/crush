//go:build linux

package sandbox

// backendsForPlatform returns the backends to probe, in preference order.
// bubblewrap is preferred because it can subtract protected paths from a
// writable root; Landlock is the alternative.
func backendsForPlatform() []Backend {
	return []Backend{&bubblewrapBackend{}, &landlockBackend{}}
}
