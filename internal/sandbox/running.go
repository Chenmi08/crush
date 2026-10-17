package sandbox

import (
	"bytes"
	"context"
	"errors"
	"os/exec"
)

// ActiveRun carries what the exec layer needs to classify a confined command's
// exit: the backend that wrapped it and a buffer for its diagnostics.
type ActiveRun struct {
	backendName string
	diagnostics bytes.Buffer
}

// StderrTee returns a writer that tees diagnostics into the run's buffer while
// forwarding to original. Pass nil original to only capture.
// BeginRun records which backend wrapped the command, so its exit can be
// classified against that backend's own signatures. A nil policy or a policy
// that does not confine returns nil: there is nothing to classify.
func BeginRun(ctx context.Context, policy *Policy, cmd *exec.Cmd) (*ActiveRun, func(), error) {
	cleanup, err := WrapCommand(ctx, policy, cmd)
	if err != nil {
		return nil, nil, err
	}
	if policy == nil || policy.Mode == ModeDangerFullAccess {
		return nil, cleanup, nil
	}
	sel := Select(ctx)
	if sel.Backend == nil {
		return nil, cleanup, nil
	}
	return &ActiveRun{backendName: sel.Backend.Name()}, cleanup, nil
}

// StderrTee returns a writer that tees the command's diagnostics into the
// run's buffer, so the original stream is untouched.
func (r *ActiveRun) StderrTee(original interface{ Write([]byte) (int, error) }) interface{ Write([]byte) (int, error) } {
	if r == nil {
		return original
	}
	return &teeWriter{to: r, original: original}
}

// teeWriter writes to the original stream and the run's diagnostics buffer.
type teeWriter struct {
	to       *ActiveRun
	original interface{ Write([]byte) (int, error) }
}

func (w *teeWriter) Write(p []byte) (int, error) {
	if w.to != nil {
		_, _ = w.to.diagnostics.Write(p)
	}
	if w.original != nil {
		return w.original.Write(p)
	}
	return len(p), nil
}

// DiagnoseClassified classifies a non-zero exit. It returns an empty
// diagnosis for an ordinary failure or a run without classification.
func (r *ActiveRun) DiagnoseClassified(exitCode int) ExitDiagnosis {
	if r == nil || exitCode == 0 {
		return ExitDiagnosis{}
	}
	d := ClassifyExit(r.backendName, exitCode, r.diagnostics.String())
	if d.Class == ExitClassOrdinary {
		return ExitDiagnosis{}
	}
	logExit(d)
	return d
}

// BackendName returns the backend that wrapped the command.
func (r *ActiveRun) BackendName() string {
	if r == nil {
		return ""
	}
	return r.backendName
}

// Diagnostics returns the captured diagnostic output.
func (r *ActiveRun) Diagnostics() string {
	if r == nil {
		return ""
	}
	return r.diagnostics.String()
}

func logExit(d ExitDiagnosis) {
	slogInfo("Sandbox classified command exit", "class", d.Class, "backend", d.Backend, "exit", d.ExitCode, "detail", d.Detail)
}

// ExitCode extracts a non-zero exit status from an execution error. It is a
// small wrapper around the shell package's convention so classification works
// without a dependency from sandbox on shell.
func ExitCode(err error) int {
	if err == nil {
		return 0
	}
	var status interface{ ExitCode() int }
	if errors.As(err, &status) {
		return status.ExitCode()
	}
	return 1
}

// IsInterrupt reports whether err is a context cancellation rather than a
// command failure.
func IsInterrupt(err error) bool {
	return errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded)
}
