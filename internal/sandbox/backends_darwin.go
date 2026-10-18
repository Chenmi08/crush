//go:build darwin

package sandbox

// backendsForPlatform returns the backends to probe on macOS.
func backendsForPlatform() []Backend {
	return []Backend{&seatbeltBackend{}}
}
