//go:build darwin

package sandbox

// backendsForPlatform returns the backends to probe on macOS.
func backendsForPlatform() []Backend {
	// Seatbelt first, then the in-process fence so the file tools and the
	// interpreter are fenced even when sandbox-exec cannot be used.
	return []Backend{&seatbeltBackend{}, fenceBackend{}}
}
