//go:build windows

package shell

import (
	"context"
	"fmt"
	"os/exec"
	"time"

	"github.com/charmbracelet/crush/internal/sandbox"
	"mvdan.cc/sh/v3/interp"
)

// defaultKillTimeout matches mvdan's DefaultExecHandler default.
const defaultKillTimeout = 2 * time.Second

// isolateProcess is a no-op on Windows. Session isolation via Setsid is a
// Unix-only concept; Windows uses CREATE_NEW_PROCESS_GROUP which mvdan's
// default handler already handles adequately.
func isolateProcess(_ *exec.Cmd) {}

// processGroupExecHandler on Windows applies the sandbox backend (which fails
// closed on this platform) and then delegates to interp.DefaultExecHandler.
func processGroupExecHandler(killTimeout time.Duration, policy *sandbox.Policy) interp.ExecHandlerFunc {
	base := interp.DefaultExecHandler(killTimeout)
	return func(ctx context.Context, args []string) error {
		// Windows has no backend: a confined policy must refuse rather than
		// run unconfined.
		if policy != nil && policy.Mode != sandbox.ModeDangerFullAccess {
			hc := interp.HandlerCtx(ctx)
			err := sandbox.WrapCommand(ctx, policy, &exec.Cmd{Args: args})
			if err != nil {
				fmt.Fprintf(hc.Stderr, "crush: %v\n", err)
				return interp.ExitStatus(126)
			}
		}
		return base(ctx, args)
	}
}
