//go:build windows

package sandbox

// backendsForPlatform returns no backend on Windows: a sandbox is reported as
// unsupported and confined commands fail closed.
func backendsForPlatform() []Backend { return []Backend{fenceBackend{}} }
