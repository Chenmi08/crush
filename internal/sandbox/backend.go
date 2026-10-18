package sandbox

import (
	"context"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"sync"
)

// As is a tiny wrapper so the rest of the package reads uniformly.
func As[T any](err error, target *T) bool { return errors.As(err, target) }

// Backend is an OS primitive that confines a child process to a policy.
//
// The boundary applies to the process and everything it starts. A backend
// never decides policy: it consumes a *Policy and translates it.
type Backend interface {
	// Name identifies the backend ("bubblewrap", "seatbelt", "landlock").
	Name() string
	// Enforcement reports the strength of the boundary this backend can
	// establish for the policy, given its own limitations.
	Enforcement(p *Policy) Enforcement
	// Wrap rewrites cmd in place so the boundary applies to it. The returned
	// cleanup func, when non-nil, must run after the command exits; it
	// removes any artifacts the backend created to express the policy (for
	// example mount points for protected paths that did not exist yet). A
	// non-nil error means the boundary could not be established and the
	// command must not run: it is a runner failure, not a policy denial.
	Wrap(ctx context.Context, p *Policy, cmd *exec.Cmd) (cleanup func(), err error)
	// Available reports whether the backend can run on this host. It must be
	// a functional probe, not a version check.
	Available(ctx context.Context) error
}

// RunnerError is a failure to establish the boundary. It is classified
// separately from a policy denial because the command never ran, while a
// denial means confinement worked.
type RunnerError struct {
	Backend  string
	Platform string
	Detail   string
	Err      error
}

func (e *RunnerError) Error() string {
	msg := fmt.Sprintf("sandbox-unavailable backend=%s platform=%s", e.Backend, e.Platform)
	if e.Detail != "" {
		msg += " detail=" + e.Detail
	}
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	return msg
}

func (e *RunnerError) Unwrap() error { return e.Err }

// IsRunnerError reports whether err is a failure to establish the boundary.
// A policy denial wrapped inside a runner error is still a runner failure:
// the command never ran, so the model must not escalate around it.
func IsRunnerError(err error) bool {
	var r *RunnerError
	return errors.As(err, &r)
}

// Selection is the result of probing for a backend.
type Selection struct {
	Backend     Backend
	Enforcement Enforcement
	// Reason explains a nil Backend: which dependency is missing and how to
	// fix it.
	Reason string
}

var (
	selectionOnce sync.Once
	selection     Selection
)

// Select probes the available backends once per process and caches the
// choice. A confined mode with no usable backend is refused at execution
// time; this function only reports the fact.
func Select(ctx context.Context) Selection {
	selectionOnce.Do(func() {
		selection = probe(ctx)
		enforcement := selection.Enforcement
		var name string
		if selection.Backend != nil {
			name = selection.Backend.Name()
		}
		logSelection(name, enforcement, selection.Reason)
	})
	return selection
}

// ResetSelectionForTest clears the cached probe. It exists for tests that
// inject an unavailable backend.
func ResetSelectionForTest() {
	selectionOnce = sync.Once{}
	selection = Selection{}
}

func probe(ctx context.Context) Selection {
	candidates := backendsForPlatform()
	if len(candidates) == 0 {
		return Selection{
			Enforcement: EnforcementNone,
			Reason: fmt.Sprintf(
				"no sandbox backend exists for %s. Sandboxing is unsupported here; "+
					"run with sandbox mode danger-full-access (or the --no-sandbox flag) to run unconfined",
				runtime.GOOS,
			),
		}
	}

	var failures []string
	for _, candidate := range candidates {
		if err := candidate.Available(ctx); err != nil {
			failures = append(failures, fmt.Sprintf("%s: %v", candidate.Name(), err))
			continue
		}
		return Selection{
			Backend:     candidate,
			Enforcement: candidate.Enforcement(nil),
		}
	}

	return Selection{
		Enforcement: EnforcementNone,
		Reason: fmt.Sprintf(
			"no usable sandbox backend on %s (%s). Install bubblewrap (bwrap) or run with "+
				"sandbox mode danger-full-access (or the --no-sandbox flag) to run unconfined",
			runtime.GOOS, joinFailures(failures),
		),
	}
}

func joinFailures(failures []string) string {
	out := ""
	for i, f := range failures {
		if i > 0 {
			out += "; "
		}
		out += f
	}
	return out
}

// WrapCommand applies the selected backend to cmd. A nil policy, or
// danger-full-access, leaves the command untouched.
//
// When no backend is usable the command is refused: a confined mode never
// silently runs unconfined.
func WrapCommand(ctx context.Context, p *Policy, cmd *exec.Cmd) (cleanup func(), err error) {
	if p == nil || p.Mode == ModeDangerFullAccess {
		return nil, nil
	}
	sel := Select(ctx)
	if sel.Backend == nil {
		return nil, &RunnerError{
			Backend:  "none",
			Platform: runtime.GOOS,
			Detail:   sel.Reason + " (or disable the sandbox explicitly with --no-sandbox, or set sandbox mode danger-full-access)",
		}
	}
	cleanup, err = sel.Backend.Wrap(ctx, p, cmd)
	if err != nil {
		return nil, &RunnerError{
			Backend:  sel.Backend.Name(),
			Platform: runtime.GOOS,
			Detail:   "the backend could not apply its profile",
			Err:      err,
		}
	}
	return cleanup, nil
}

// Status is the resolved policy plus the enforcement facts, suitable for the
// status line and the runtime-state output the agent can request.
type Status struct {
	Policy      *Policy
	Backend     string
	Enforcement Enforcement
	// Missing names the guarantee that is not enforced, when partial.
	Missing string
	// Note is a human-readable summary.
	Note string
	// Unsupported is set when the platform has no backend at all.
	Unsupported bool
}

// Describe resolves the enforcement facts for a policy without running
// anything.
func Describe(ctx context.Context, p *Policy) Status {
	sel := Select(ctx)
	status := Status{Policy: p, Backend: "none", Enforcement: EnforcementNone}
	if p == nil {
		status.Note = "no sandbox policy attached; commands run unconfined"
		return status
	}
	if p.Mode == ModeDangerFullAccess {
		status.Backend = "none"
		status.Note = "sandbox disabled: no confinement"
		return status
	}
	if sel.Backend == nil {
		status.Unsupported = runtime.GOOS == "windows"
		status.Missing = sel.Reason
		status.Note = "sandbox unavailable: confined commands are refused"
		return status
	}
	status.Backend = sel.Backend.Name()
	status.Enforcement = sel.Backend.Enforcement(p)
	status.Note = status.Enforcement.Note()
	if status.Enforcement == EnforcementPartial {
		status.Missing = partialMissing(p, status.Backend)
	}
	return status
}

// Note renders the enforcement level for a status line.
func (e Enforcement) Note() string {
	switch e {
	case EnforcementFull:
		return "enforced"
	case EnforcementPartial:
		return "enforced partially"
	default:
		return "not enforced"
	}
}

// String renders a one-line status suitable for a status bar.
func (s Status) String() string {
	if s.Policy == nil {
		return "sandbox=off"
	}
	return fmt.Sprintf("sandbox=%s enforcement=%s backend=%s", s.Policy.Mode, s.Enforcement, s.Backend)
}

func partialMissing(p *Policy, backend string) string {
	switch {
	case p != nil && p.RequiresSubtraction() && backend == "landlock":
		return "protected paths inside writable roots cannot be subtracted"
	case backend == "":
		return "no backend"
	default:
		return "a guarantee the policy asks for is not enforced"
	}
}

// ApplyEnv sets environment variables inside cmd.Env that make tools respect
// the private temp directory instead of a shared /tmp.
func (p *Policy) ApplyEnv(env []string) []string {
	if p == nil || p.TempDir == "" {
		return env
	}
	return append(env,
		"TMPDIR="+p.TempDir,
		"TMP="+p.TempDir,
		"TEMP="+p.TempDir,
	)
}

// protectedPlaceholder returns a path to an empty file that a backend can bind
// over a protected path that does not exist yet, so the confined process
// cannot create it. The file lives in the policy's private temp directory, so
// it is removed with the rest of the run's state.
func (p *Policy) protectedPlaceholder() (string, error) {
	if p == nil || p.TempDir == "" {
		return "", errors.New("sandbox: no private temp directory for protected-path placeholders")
	}
	dir := filepath.Join(p.TempDir, ".placeholders")
	if err := os.MkdirAll(dir, 0o700); err != nil {
		return "", fmt.Errorf("sandbox: creating placeholder directory: %w", err)
	}
	path := filepath.Join(dir, "protected")
	f, err := os.OpenFile(path, os.O_CREATE|os.O_WRONLY, 0o600)
	if err != nil {
		return "", fmt.Errorf("sandbox: creating protected-path placeholder: %w", err)
	}
	if err := f.Close(); err != nil {
		return "", err
	}
	return path, nil
}

// availableOnPath is a small helper for functional probes.
func availableOnPath(name string) (string, bool) {
	path, err := exec.LookPath(name)
	if err != nil {
		return "", false
	}
	if abs, err := filepath.Abs(path); err == nil {
		return abs, true
	}
	return path, true
}

// ForceSelectionForTest overrides the cached backend selection. It exists so
// tests can assert fail-closed and prompt behaviour without uninstalling a
// backend; production code never calls it.
func ForceSelectionForTest(sel Selection) {
	selectionOnce.Do(func() {})
	selection = sel
}
