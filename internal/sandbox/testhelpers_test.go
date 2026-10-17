package sandbox

import "testing"

// forceSelection overrides the cached backend selection for one test. It is
// shared by the platform-specific behavior tests, which need an unavailable
// backend to assert fail-closed behavior without uninstalling anything.
func forceSelection(t *testing.T, sel Selection) {
	t.Helper()
	selectionOnce.Do(func() {}) // mark done so probe never runs
	selection = sel
}
