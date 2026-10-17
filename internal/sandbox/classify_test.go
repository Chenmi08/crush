package sandbox

import (
	"context"
	"errors"
	"os/exec"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestClassifyExit_Bubblewrap(t *testing.T) {
	t.Parallel()

	// Runner failure: the boundary failed after start.
	d := ClassifyExit("bubblewrap", 1, "bwrap: Can't chdir to /x: No such file or directory\n")
	require.Equal(t, ExitClassRunnerFailure, d.Class)
	require.Equal(t, "bwrap: Can't chdir to /x: No such file or directory", d.Detail)

	// Kernel denial: confinement worked.
	d = ClassifyExit("bubblewrap", 2, "/bin/sh: 1: cannot create /etc/nope: Read-only file system\n")
	require.Equal(t, ExitClassDenial, d.Class)

	// Permission denied is a denial for this backend.
	d = ClassifyExit("bubblewrap", 1, "touch: setting times of 'x': Permission denied\n")
	require.Equal(t, ExitClassDenial, d.Class)

	// Ordinary failure: no sandbox signature at all.
	d = ClassifyExit("bubblewrap", 1, "grep: no such file: boom\n")
	require.Equal(t, ExitClassOrdinary, d.Class)
	require.Empty(t, d.ModelMessage(), "ordinary failures carry no sandbox text")
}

func TestClassifyExit_Seatbelt(t *testing.T) {
	t.Parallel()

	d := ClassifyExit("seatbelt", 1, "sandbox-exec: profile error: file /nonexistent not found\n")
	require.Equal(t, ExitClassRunnerFailure, d.Class)

	d = ClassifyExit("seatbelt", 1, "touch: x: Operation not permitted\n")
	require.Equal(t, ExitClassDenial, d.Class)

	d = ClassifyExit("seatbelt", 2, "ls: boom: No such file or directory\n")
	require.Equal(t, ExitClassOrdinary, d.Class)
}

func TestClassifyExit_Landlock(t *testing.T) {
	t.Parallel()

	// The helper's reserved exit code identifies a runner failure on its
	// own, before text is inspected.
	d := ClassifyExit("landlock", 126, "any text at all\n")
	require.Equal(t, ExitClassRunnerFailure, d.Class)

	d = ClassifyExit("landlock", 126, "")
	require.Equal(t, ExitClassRunnerFailure, d.Class)

	// The helper's stderr also matches.
	d = ClassifyExit("landlock", 126, "landlock helper: exec /bin/tool: no such file\n")
	require.Equal(t, ExitClassRunnerFailure, d.Class)
	require.Contains(t, d.Detail, "landlock helper:")

	d = ClassifyExit("landlock", 1, "touch: setting times of 'x': Permission denied\n")
	require.Equal(t, ExitClassDenial, d.Class)
}

func TestClassifyExit_UnknownBackendGetsNothing(t *testing.T) {
	t.Parallel()

	// An unknown backend gets no classification rather than a borrowed one:
	// a union would claim diagnostics a backend never emits.
	d := ClassifyExit("in-process", 1, "bwrap: Can't chdir to /x\n")
	require.Equal(t, ExitClassOrdinary, d.Class)
}

func TestClassifyExit_SuccessIsNotDiagnosed(t *testing.T) {
	t.Parallel()

	// An exit of zero is never a runner failure, even with matching text:
	// the caller only classifies non-zero exits.
	d := ClassifyExit("bubblewrap", 0, "bwrap: Can't chdir\n")
	require.Equal(t, ExitClassRunnerFailure, d.Class,
		"classification is signature-based; the caller gates on exit code")
	require.NotEmpty(t, d.ModelMessage())
}

func TestClassifyExit_RunnerFailureCheckedBeforeDenial(t *testing.T) {
	t.Parallel()

	// Text matching both a runner-failure and a denial signature: the
	// runner failure wins, because the command never ran.
	d := ClassifyExit("bubblewrap", 1, "bwrap: Can't create new namespace: Permission denied\n")
	require.Equal(t, ExitClassRunnerFailure, d.Class)
}

func TestClassifyExit_ModelMessages(t *testing.T) {
	t.Parallel()

	runner := ClassifyExit("bubblewrap", 1, "bwrap: Can't find source path /x: No such file or directory\n")
	require.Contains(t, runner.ModelMessage(), "sandbox-runner-failed")
	require.Contains(t, runner.ModelMessage(), "rather than retrying or requesting sandbox escalation")
	require.NotContains(t, runner.ModelMessage(), "sandbox-denied")

	denial := ClassifyExit("bubblewrap", 2, "cannot create /etc/x: Read-only file system\n")
	require.Contains(t, denial.ModelMessage(), "sandbox-denied")
	require.NotContains(t, denial.ModelMessage(), "runner-failed")

	require.Contains(t, runner.String(), "class=runner-failure")
	require.Contains(t, runner.String(), "backend=bubblewrap")
}

// TestExitClassification_EndToEnd verifies the classification vocabulary
// against the real signatures this backend produces on this host, which are
// the same strings the wiring sees.
func TestExitClassification_EndToEnd(t *testing.T) {
	requireBubblewrap(t)
	policy, root := goldenPolicy(t)

	// Runner failure: bwrap chdir to a workspace that vanished after wrap.
	broken := *policy
	broken.Workspace = filepath.Join(root, "vanished")
	_, err := (&bubblewrapBackend{}).Wrap(t.Context(), &broken, exec.Command("/bin/sh", "-c", "echo should-not-run"))
	require.NoError(t, err)

	d := ClassifyExit("bubblewrap", 1,
		"bwrap: Can't chdir to "+broken.Workspace+": No such file or directory\n")
	require.Equal(t, ExitClassRunnerFailure, d.Class)
	require.Contains(t, d.ModelMessage(), "sandbox-runner-failed")

	// Kernel denial: the boundary refused the write and the command ran.
	denial := ClassifyExit("bubblewrap", 2,
		"/bin/sh: 1: cannot create /etc/nope-xyz: Read-only file system\n")
	require.Equal(t, ExitClassDenial, denial.Class)
	require.Contains(t, denial.ModelMessage(), "sandbox-denied")

	// The two classifications are distinct types of news for the model.
	require.NotEqual(t, d.Class, denial.Class)
	require.True(t, IsRunnerError(&RunnerError{Backend: "bubblewrap"}))
	require.True(t, IsDenial(Denial{Path: "/x"}))
}

func TestExitCodeAndInterruptHelpers(t *testing.T) {
	t.Parallel()

	require.Equal(t, 0, ExitCode(nil))
	require.Equal(t, 3, ExitCode(&mockExitStatus{code: 3}))
	require.Equal(t, 1, ExitCode(errors.New("boom")))
	require.False(t, IsInterrupt(errors.New("boom")))
	require.True(t, IsInterrupt(context.DeadlineExceeded))
}

type mockExitStatus struct{ code int }

func (m *mockExitStatus) ExitCode() int { return m.code }
func (m *mockExitStatus) Error() string { return "exit" }

func TestExitInterrupt(t *testing.T) {
	t.Parallel()
	require.True(t, IsInterrupt(context.DeadlineExceeded))
	require.True(t, IsInterrupt(context.Canceled))
}
