//go:build !linux

package sandbox

import (
	"fmt"
	"os"
)

// RunLandlockHelper is a no-op on platforms without Landlock: the backend is
// Linux-only, so nothing ever sets the helper environment or re-executes the
// binary with a target command. If it is somehow invoked, refuse loudly rather
// than silently running the command unconfined.
func RunLandlockHelper(args []string) (handled bool, code int) {
	if len(args) == 0 {
		return true, 126
	}
	fmt.Fprintf(os.Stderr, "landlock helper is not available on this platform\n")
	return true, 126
}

// ExecutablePath mirrors the Linux seam so shared test helpers compile.
var ExecutablePath = os.Executable
