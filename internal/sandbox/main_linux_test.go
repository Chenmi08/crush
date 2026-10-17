//go:build linux

package sandbox

import (
	"os"
	"testing"
)

// TestMain turns the test binary into a Landlock helper when the helper
// environment is present. This is how the behavior tests exercise the real
// helper without depending on the Crush binary: the backend re-executes
// ExecutablePath(), which the tests point at this binary.
func TestMain(m *testing.M) {
	if handled, code := RunLandlockHelper(os.Args[1:]); handled {
		os.Exit(code)
	}
	os.Exit(m.Run())
}
