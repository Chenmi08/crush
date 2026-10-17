//go:build windows

package sandbox

// backendsForPlatform returns no backend on Windows: a sandbox is reported as
// unsupported and confined commands fail closed rather than running
// unconfined.
func backendsForPlatform() []Backend { return nil }
