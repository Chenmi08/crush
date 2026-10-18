package tools

import (
	"log/slog"
	"os/exec"
	"sync"
	"testing"

	"github.com/charmbracelet/crush/internal/log"
)

var getRg = sync.OnceValue(func() string {
	if testing.Testing() {
		return ""
	}
	path, err := exec.LookPath("rg")
	if err != nil {
		if log.Initialized() {
			slog.Warn("Ripgrep (rg) not found in $PATH. Some grep features might be limited or slower.")
		}
		return ""
	}
	return path
})

// fdCommands are the names fd ships under. Some Linux distributions install
// it as "fdfind" because an unrelated package already owns "fd".
var fdCommands = []string{"fd", "fdfind"}

// getFd returns the command name for fd, or an empty string when fd is
// unavailable. Unlike getRg it returns the name rather than the absolute
// path, because the result is surfaced in the bash tool description for the
// model to invoke.
var getFd = sync.OnceValue(func() string {
	if testing.Testing() {
		return ""
	}
	for _, name := range fdCommands {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}
	if log.Initialized() {
		slog.Warn("The fd command was not found in $PATH. File finding will fall back to find or rg --files.")
	}
	return ""
})
