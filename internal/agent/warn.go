package agent

import (
	"fmt"
	"os"
	"sync"
)

// fmtWarnOnce guards the sandbox-disabled warning so a session that toggles
// modes repeatedly prints it once.
var fmtWarnOnce sync.Once

// fmtWarn writes a warning to stderr.
func fmtWarn(msg string) {
	fmt.Fprint(os.Stderr, msg)
}
