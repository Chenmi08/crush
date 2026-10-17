//go:build linux

package sandbox

// backendsForPlatform returns the backends to probe, in preference order.
// bubblewrap is preferred because it can subtract protected paths from a
// writable root; Landlock is the fallback.
func backendsForPlatform() []Backend {
	// Until the OS backends are wired, the in-process fence is the only
	// enforcement point: writes through Crush's tools and interpreter are
	// fenced, child processes are not.
	return []Backend{fenceBackend{}}
}
