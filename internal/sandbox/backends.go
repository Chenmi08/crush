//go:build !linux && !darwin && !windows

package sandbox

// backendsForPlatform returns the backends to probe for a platform with no
// implementation.
func backendsForPlatform() []Backend { return nil }
