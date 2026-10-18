package shell

import (
	"bytes"
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"os"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/sandbox"

	"mvdan.cc/sh/v3/expand"
	"mvdan.cc/sh/v3/interp"
	"mvdan.cc/sh/v3/syntax"
)

// RunOptions configures a single stateless shell execution via [Run].
//
// The zero value is not useful; at minimum Command must be set. Stdin,
// Stdout, and Stderr may be nil (nil readers/writers are treated as
// empty/discard). Hooks use Run to execute user-authored commands with the
// same trust level as a shell alias.
type RunOptions struct {
	// Command is the shell source to parse and execute.
	Command string
	// Cwd is the working directory for the execution. Required: callers
	// must supply a non-empty value. Run does not silently fall back to
	// the Crush process cwd — hooks and the bash tool have different
	// notions of "default" and each owns that decision.
	Cwd string
	// Env is the full environment visible to the command. The caller is
	// responsible for inheriting from os.Environ() if that's desired.
	Env []string
	// Stdin is the command's standard input. nil is equivalent to an empty
	// input stream.
	Stdin io.Reader
	// Stdout receives the command's standard output. nil discards output.
	Stdout io.Writer
	// Stderr receives the command's standard error. nil discards output.
	Stderr io.Writer
	// TermWidth is the terminal width in columns for PTY execution.
	// Zero uses a default of 200.
	TermWidth int
	// Policy is the sandbox policy carried by this execution. It is carried
	// per execution rather than stored globally so concurrent sessions and
	// one-shot escalations can hold different policies at once. Nil means no
	// fence is attached.
	Policy *sandbox.Policy
}

// Run parses and executes a shell command using the same mvdan.cc/sh
// interpreter stack that the stateful [Shell] type uses (builtins,
// optional block list, optional Go coreutils). It is safe to call
// concurrently from multiple goroutines: each call builds its own
// [interp.Runner] and shares no state with other callers or with any
// [Shell] instance.
//
// Errors returned from the command itself (non-zero exit, context
// cancellation, parse failures) follow the same conventions as
// [Shell.Exec]: inspect with [IsInterrupt] and [ExitCode].
func Run(ctx context.Context, opts RunOptions) (err error) {
	defer func() {
		if r := recover(); r != nil {
			err = fmt.Errorf("command execution panic: %v", r)
		}
	}()

	if opts.Cwd == "" {
		return fmt.Errorf("shell.Run: Cwd is required")
	}

	stdout := opts.Stdout
	if stdout == nil {
		stdout = io.Discard
	}
	stderr := opts.Stderr
	if stderr == nil {
		stderr = io.Discard
	}

	line, err := syntax.NewParser().Parse(strings.NewReader(opts.Command), "")
	if err != nil {
		return fmt.Errorf("could not parse command: %w", err)
	}

	runner, err := newRunnerWithPolicy(opts.Cwd, opts.Env, opts.Stdin, stdout, stderr, opts.Policy)
	if err != nil {
		return fmt.Errorf("could not run command: %w", err)
	}

	return runner.Run(ctx, line)
}

// CaptureResult holds the combined output and exit code from a
// captured shell execution.
type CaptureResult struct {
	Output   string
	ExitCode int
}

// PersistFunc is a callback that persists a shell command result.
// Used by RunAndPersist to decouple execution from storage.
type PersistFunc func(command, output string, exitCode int) error

// RunAndPersist executes a shell command via PTY and optionally
// persists the result through the provided callback. This unifies
// the run-and-save pattern used by both AppWorkspace and Backend.
func RunAndPersist(ctx context.Context, opts RunOptions, persist PersistFunc) (CaptureResult, error) {
	result, err := RunAndCapturePTY(ctx, opts)
	if err != nil {
		return CaptureResult{}, err
	}

	if persist != nil {
		if persistErr := persist(opts.Command, result.Output, result.ExitCode); persistErr != nil {
			slog.Error("Failed to persist shell command output", "error", persistErr, "command", opts.Command)
		}
	}

	return result, nil
}

// RunAndCapture executes a shell command and returns its combined
// stdout/stderr output along with the exit code. It inherits the
// current process environment when opts.Env is nil.
func RunAndCapture(ctx context.Context, opts RunOptions) (CaptureResult, error) {
	if opts.Env == nil {
		opts.Env = os.Environ()
	}

	var stdout, stderr bytes.Buffer
	opts.Stdout = &stdout
	opts.Stderr = &stderr

	runErr := Run(ctx, opts)

	exitCode := 0
	if runErr != nil {
		exitCode = ExitCode(runErr)
	}

	output := stdout.String()
	if stderr.Len() > 0 {
		if output != "" {
			output += "\n"
		}
		output += stderr.String()
	}

	return CaptureResult{
		Output:   output,
		ExitCode: exitCode,
	}, nil
}

// ptyColorEnvVars force color output for programs running inside a
// PTY. These are only applied in RunAndCapturePTY, not in the plain
// Run/RunAndCapture paths where ANSI codes would be noise.
var ptyColorEnvVars = []string{
	"COLORTERM=truecolor",
	"CLICOLOR_FORCE=1",
	"FORCE_COLOR=1",
}

// RunAndCapturePTY executes a shell command through the mvdan.cc/sh
// interpreter with color-forcing environment variables set. Programs
// that respect FORCE_COLOR or CLICOLOR_FORCE (git, cargo, npm, eza,
// bat, ripgrep, etc.) will emit ANSI color sequences even without a
// real PTY. This approach is fully cross-platform — no /bin/sh or
// unix PTY required.
//
// The name is preserved for API compatibility; the PTY path has been
// replaced by the portable interpreter + env-var approach.
func RunAndCapturePTY(ctx context.Context, opts RunOptions) (CaptureResult, error) {
	if opts.Env == nil {
		opts.Env = os.Environ()
	}
	opts.Env = append(opts.Env, ptyColorEnvVars...)
	return RunAndCapture(ctx, opts)
}

// newRunner constructs an [interp.Runner] configured with the standard
// Crush handler stack. Shared by the stateless [Run] entrypoint and the
// stateful [Shell] so the two surfaces cannot drift.
// newRunnerWithPolicy constructs an [interp.Runner] configured with the
// standard Crush handler stack and, when a policy is attached, the
// write-intent file-open hook; read paths, directory listing, and stat keep
// their defaults because reads are unconfined. Shared by the stateless [Run]
// entrypoint and the stateful [Shell] so the two surfaces cannot drift.
func newRunnerWithPolicy(cwd string, env []string, stdin io.Reader, stdout, stderr io.Writer, policy *sandbox.Policy) (*interp.Runner, error) {
	env = withNonInteractiveEnv(policy.ApplyEnv(env))
	opts := []interp.RunnerOption{
		interp.StdIO(stdin, stdout, stderr),
		interp.Interactive(false),
		interp.Env(expand.ListEnviron(env...)),
		interp.Dir(cwd),
		execHandlerOption(policy),
	}
	if policy != nil {
		opts = append(opts, interp.OpenHandler(openHandler(policy)))
	}
	return interp.New(opts...)
}

// openHandler returns the file-open hook that fences write intent against the
// policy. The in-process interpreter performs redirects, sourcing, and
// builtin file opens itself, so wrapping child processes alone would leave
// `echo x > /tmp/y` unconfined.
//
// Only write intent is checked: an open without O_WRONLY/O_RDWR/O_CREATE/
// O_TRUNC/O_APPEND is a read and is left alone, because reads are
// unconfined. The refusal is an *os.PathError so the interpreter reports it on
// stderr with a non-zero status, exactly as a kernel denial would, and the
// message carries the shared denial marker.
//
// Relative paths are resolved against the interpreter's current directory
// (HandlerCtx.Dir), not against Crush's process working directory: the two
// differ after `cd`, and the interpreter itself opens the path relative to the
// former.
func openHandler(policy *sandbox.Policy) interp.OpenHandlerFunc {
	def := interp.DefaultOpenHandler()
	return func(ctx context.Context, path string, flag int, perm os.FileMode) (io.ReadWriteCloser, error) {
		if writeIntent(flag) {
			if err := policy.CheckWriteIn(interp.HandlerCtx(ctx).Dir, path); err != nil {
				if d, ok := sandbox.AsDenial(err); ok {
					sandbox.LogDenial(d)
					return nil, &os.PathError{Op: "open", Path: path, Err: errors.New(d.Error())}
				}
				return nil, &os.PathError{Op: "open", Path: path, Err: err}
			}
		}
		return def(ctx, path, flag, perm)
	}
}

// writeIntent reports whether an open flag combination can modify the
// filesystem.
func writeIntent(flag int) bool {
	const writeFlags = os.O_WRONLY | os.O_RDWR | os.O_CREATE | os.O_TRUNC | os.O_APPEND
	return flag&writeFlags != 0
}

// execHandlerOption returns an interp.RunnerOption that installs the
// standard Crush middleware chain (builtins, script dispatch, block list)
// on top of a process-group-isolated base exec handler.
//
// We use interp.ExecHandler (singular) with a manually-built chain rather
// than interp.ExecHandlers because the latter always appends
// interp.DefaultExecHandler as the final handler, which lacks process group
// isolation. Without isolation, shells like zsh that set up job control
// when sourcing framework files can send SIGINT/SIGTERM to Crush's process
// group and crash the parent.
func execHandlerOption(policy *sandbox.Policy) interp.RunnerOption {
	base := processGroupExecHandler(defaultKillTimeout, policy)
	handler := base
	for _, mw := range slices.Backward(standardHandlers(policy)) {
		handler = mw(handler)
	}
	// ExecHandlers always appends DefaultExecHandler which lacks process
	// group isolation, so we use the deprecated ExecHandler instead.
	return interp.ExecHandler(handler)
}

// nonInteractiveEnvVars are forced on every shell execution to prevent
// commands from hanging on a nonexistent TTY. These are always applied
// regardless of the caller's environment because Crush shells are never
// interactive — preserving user preferences like EDITOR=nvim only causes
// hangs, not useful behavior.
var nonInteractiveEnvVars = []string{
	"TERM=xterm-256color",
	"GIT_EDITOR=false",
	"EDITOR=false",
	"VISUAL=false",
	"JJ_EDITOR=false",
	"JJ_PAGER=cat",
	"GIT_PAGER=cat",
	"PAGER=cat",
}

// withNonInteractiveEnv returns env with nonInteractiveEnvVars forced in,
// replacing any existing values for those keys. The returned slice is a
// new allocation safe to use concurrently with the input.
func withNonInteractiveEnv(env []string) []string {
	// Build a set of override keys for fast lookup.
	overrideKeys := make(map[string]bool, len(nonInteractiveEnvVars))
	for _, kv := range nonInteractiveEnvVars {
		if key, _, ok := strings.Cut(kv, "="); ok {
			overrideKeys[key] = true
		}
	}

	// Copy env, filtering out any keys we will override.
	result := make([]string, 0, len(env)+len(nonInteractiveEnvVars))
	for _, e := range env {
		if key, _, ok := strings.Cut(e, "="); ok && overrideKeys[key] {
			continue
		}
		result = append(result, e)
	}

	return append(result, nonInteractiveEnvVars...)
}

// herdrEnvVars are the environment variables herdr injects into panes
// so agents can report state over its Unix socket API. Subprocesses
// must not inherit these: a child process that calls herdr.Init()
// would attach to the parent's pane and, on exit, release its agent
// authority — making the status vanish. Stripping them here closes
// that gap for every command the bash tool runs.
var herdrEnvVars = []string{
	"HERDR_ENV",
	"HERDR_SOCKET_PATH",
	"HERDR_PANE_ID",
}

// withoutHerdrEnv returns env with all HERDR_* variables removed.
// The returned slice is a new allocation safe to use concurrently
// with the input.
func withoutHerdrEnv(env []string) []string {
	strip := make(map[string]bool, len(herdrEnvVars))
	for _, k := range herdrEnvVars {
		strip[k] = true
	}
	result := make([]string, 0, len(env))
	for _, e := range env {
		if key, _, ok := strings.Cut(e, "="); ok && strip[key] {
			continue
		}
		result = append(result, e)
	}
	return result
}

// execMiddleware wraps a base [interp.ExecHandlerFunc], composing like HTTP
// middleware: each layer either handles a command itself or delegates to the
// next handler in the chain.
type execMiddleware = func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc

// standardHandlers returns the exec-handler middleware chain used by both
// [Run] and [Shell]. Order matters:
//  1. builtins first (so Crush's in-process jq wins over any PATH binary);
//  2. script dispatch (shebang / binary / shell-source for path-prefixed
//     argv[0], no-op for bare commands) — runs before the block list so
//     that deny rules see the already-resolved argv of anything the
//     script exec's rather than the outer path-prefixed wrapper;
//  3. optional Go coreutils (only when useGoCoreUtils is on).
//
// The Go coreutils are skipped when a sandbox policy is attached: they write
// files (cp, mv, rm, mkdir, touch, tar) inside the Crush process, bypassing
// both the interpreter's file-open hook and the process backend. Leaving them
// out makes those commands resolve to real binaries, which the selected
// backend confines. On a platform with no backend the command is then refused,
// which is the intended fail-closed behavior rather than an unconfined write.
func standardHandlers(policy *sandbox.Policy) []execMiddleware {
	handlers := []execMiddleware{
		builtinHandler(),
		scriptDispatchHandler(policy),
	}
	if useGoCoreUtils && coreUtilsExecHandler != nil && policy == nil {
		handlers = append(handlers, coreUtilsExecHandler)
	}
	return handlers
}

// builtinHandler returns middleware that dispatches recognized Crush
// builtins to their in-process Go implementations. All builtins (jq plus the
// config builtins registered by shellconfig) live in the builtins map; config
// builtins are no-ops without a ConfigBuilder on the context.
func builtinHandler() execMiddleware {
	return func(next interp.ExecHandlerFunc) interp.ExecHandlerFunc {
		return func(ctx context.Context, args []string) error {
			if len(args) == 0 {
				return next(ctx, args)
			}
			if h, ok := builtins[args[0]]; ok {
				hc := interp.HandlerCtx(ctx)
				return h(ctx, args, hc.Stdin, hc.Stdout, hc.Stderr)
			}
			return next(ctx, args)
		}
	}
}
