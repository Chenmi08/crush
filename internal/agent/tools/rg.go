package tools

import (
	"context"
	"log/slog"
	"os/exec"
	"path/filepath"
	"strconv"
	"strings"
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

func getRgCmd(ctx context.Context, globPattern string) *exec.Cmd {
	name := getRg()
	if name == "" {
		return nil
	}
	// Note: we intentionally do not pass -L (follow symlinks). Following
	// symlinks lets rg escape the search root (into module caches, the nix
	// store, $HOME, etc.) and chase cycles, which pins all cores and can
	// hang. This keeps glob scoped to the tree it was pointed at, matching
	// the grep search command.
	args := []string{"--files", "--null"}
	if globPattern != "" {
		if !filepath.IsAbs(globPattern) && !strings.HasPrefix(globPattern, "/") {
			globPattern = "/" + globPattern
		}
		args = append(args, "--glob", globPattern)
	}
	return exec.CommandContext(ctx, name, args...)
}

func getRgSearchCmd(ctx context.Context, opts grepOptions) *exec.Cmd {
	name := getRg()
	if name == "" {
		return nil
	}
	return exec.CommandContext(ctx, name, rgArgs(opts)...)
}

// rgArgs translates a grep request into ripgrep arguments.
func rgArgs(opts grepOptions) []string {
	// Use -n to show line numbers, -0 for null separation to handle Windows paths
	args := []string{"--json", "-H", "-n", "-0"}
	if opts.ignoreCase {
		args = append(args, "-i")
	}
	if opts.multiline {
		args = append(args, "-U", "--multiline-dotall")
	}
	if opts.context > 0 {
		args = append(args, "-C", strconv.Itoa(opts.context))
	}
	if opts.typeFilter != "" {
		args = append(args, "--type", opts.typeFilter)
	}
	if opts.include != "" {
		args = append(args, "--glob", opts.include)
	}
	return append(args, opts.pattern, opts.path)
}
