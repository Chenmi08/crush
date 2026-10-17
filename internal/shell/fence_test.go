package shell

import (
	"bytes"
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/stretchr/testify/require"
)

// fencePolicy builds a policy with a temp workspace and a private temp dir,
// both outside the parent temp directory, so "outside" paths are unambiguous.
func fencePolicy(t *testing.T) *sandbox.Policy {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	policy, err := sandbox.Resolve(sandbox.Options{
		Workspace: workspace,
		TempDir:   filepath.Join(root, "tmp"),
		CacheDir:  filepath.Join(root, "cache"),
	})
	require.NoError(t, err)
	return policy
}

func runFenced(t *testing.T, policy *sandbox.Policy, cwd, command string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(t.Context(), RunOptions{
		Command: command,
		Cwd:     cwd,
		Env:     os.Environ(),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Policy:  policy,
	})
	return stdout.String(), stderr.String(), err
}

// TestFence_RedirectOutsideRootsRefused is the core interpreter-fence test: a
// redirect never spawns a process, so wrapping child processes alone would let
// it through.
func TestFence_RedirectOutsideRootsRefused(t *testing.T) {
	t.Parallel()
	policy := fencePolicy(t)
	outside := filepath.Join(t.TempDir(), "out.txt")

	_, stderr, err := runFenced(t, policy, policy.Workspace, "echo hello > "+outside)
	require.Error(t, err)
	require.Contains(t, stderr, sandbox.Marker)
	require.Contains(t, stderr, "mode=workspace-write")

	_, statErr := os.Stat(outside)
	require.True(t, os.IsNotExist(statErr), "the redirect target must not be created")
}

func TestFence_RedirectInsideRootsAllowed(t *testing.T) {
	t.Parallel()
	policy := fencePolicy(t)
	inside := filepath.Join(policy.Workspace, "out.txt")

	_, stderr, err := runFenced(t, policy, policy.Workspace, "echo hello > "+inside)
	require.NoError(t, err, "stderr=%q", stderr)

	content, err := os.ReadFile(inside)
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(content))
}

// TestFence_AppendAndStderrRedirects covers the other redirect forms.
func TestFence_AppendAndStderrRedirects(t *testing.T) {
	t.Parallel()
	policy := fencePolicy(t)

	for name, cmd := range map[string]string{
		"append":   "echo x >> %s",
		"stderr":   "echo x 2> %s",
		"combined": "echo x &> %s",
	} {
		t.Run(name, func(t *testing.T) {
			t.Parallel()
			outside := filepath.Join(t.TempDir(), "out")
			_, stderr, err := runFenced(t, policy, policy.Workspace, sprintf(cmd, outside))
			require.Error(t, err)
			require.Contains(t, stderr, sandbox.Marker)
		})
	}
}

// TestFence_SourceIsFenced verifies that sourcing a file opened for writing is
// subject to the same check: `source` opens the file for reading, so the
// script it runs is what must be fenced, not the read.
func TestFence_SourceIsFenced(t *testing.T) {
	t.Parallel()
	policy := fencePolicy(t)
	script := filepath.Join(policy.Workspace, "script.sh")
	outside := filepath.Join(t.TempDir(), "out.txt")
	require.NoError(t, os.WriteFile(script, []byte("echo x > "+outside+"\n"), 0o644))

	_, stderr, err := runFenced(t, policy, policy.Workspace, "source "+script)
	require.Error(t, err)
	require.Contains(t, stderr, sandbox.Marker)
}

// TestFence_BuiltinWriteIsFenced covers an in-process builtin that opens a
// path for writing. jq writes to its output redirect, which the open handler
// sees.
func TestFence_BuiltinWriteIsFenced(t *testing.T) {
	t.Parallel()
	policy := fencePolicy(t)
	outside := filepath.Join(t.TempDir(), "out.json")

	_, stderr, err := runFenced(t, policy, policy.Workspace, `echo '{"a":1}' | jq . > `+outside)
	require.Error(t, err)
	require.Contains(t, stderr, sandbox.Marker)
}

// TestFence_ReadsAreUnconfined pins that reads outside the workspace still
// work: reads are deliberately not confined.
func TestFence_ReadsAreUnconfined(t *testing.T) {
	t.Parallel()
	policy := fencePolicy(t)
	outsideDir := t.TempDir()
	outside := filepath.Join(outsideDir, "readable.txt")
	require.NoError(t, os.WriteFile(outside, []byte("readable\n"), 0o644))

	stdout, stderr, err := runFenced(t, policy, policy.Workspace, "cat "+outside)
	require.NoError(t, err, "stderr=%q", stderr)
	require.Equal(t, "readable\n", stdout)

	// Directory listing and stat outside the workspace also work.
	stdout, _, err = runFenced(t, policy, policy.Workspace, "ls "+outsideDir)
	require.NoError(t, err)
	require.Contains(t, stdout, "readable.txt")

	_, _, err = runFenced(t, policy, policy.Workspace, "test -f "+outside)
	require.NoError(t, err)
}

// TestFence_StatefulShellUsesPolicy verifies the stateful shell carries the
// policy across executions.
func TestFence_StatefulShellUsesPolicy(t *testing.T) {
	t.Parallel()
	policy := fencePolicy(t)
	sh := NewShell(&Options{WorkingDir: policy.Workspace, Policy: policy})

	outside := filepath.Join(t.TempDir(), "out.txt")
	_, stderr, err := sh.Exec(t.Context(), "echo x > "+outside)
	require.Error(t, err)
	require.Contains(t, stderr, sandbox.Marker)

	inside := filepath.Join(policy.Workspace, "ok.txt")
	_, _, err = sh.Exec(t.Context(), "echo ok > "+inside)
	require.NoError(t, err)
}

// TestFence_BackgroundJobCarriesPolicy verifies a background job keeps its
// confinement after the tool returns.
func TestFence_BackgroundJobCarriesPolicy(t *testing.T) {
	t.Parallel()
	policy := fencePolicy(t)
	manager := testBackgroundShellManager(t)

	outside := filepath.Join(t.TempDir(), "bg.txt")
	job, err := manager.StartWithPolicy(t.Context(), policy.Workspace, nil, policy,
		"echo x > "+outside, "fenced background job")
	require.NoError(t, err)
	job.Wait()

	_, _, _, _ = job.GetOutput()
	_, statErr := os.Stat(outside)
	require.True(t, os.IsNotExist(statErr), "a background job must not outlive its confinement")

	inside := filepath.Join(policy.Workspace, "bg.txt")
	job, err = manager.StartWithPolicy(t.Context(), policy.Workspace, nil, policy,
		"echo ok > "+inside, "allowed background job")
	require.NoError(t, err)
	job.Wait()
	_, err = os.Stat(inside)
	require.NoError(t, err)
}

// TestFence_ConcurrentPoliciesDoNotInterfere pins that the policy is carried
// per execution rather than stored globally.
func TestFence_ConcurrentPoliciesDoNotInterfere(t *testing.T) {
	t.Parallel()
	permissive := fencePolicy(t)
	restrictive := fencePolicy(t)

	shared := filepath.Join(t.TempDir(), "shared.txt")

	done := make(chan error, 2)
	go func() {
		_, _, err := runPersistent(t, permissive, permissive.Workspace, "echo x > "+shared)
		done <- err
	}()
	go func() {
		_, _, err := runPersistent(t, restrictive, restrictive.Workspace, "echo x > "+shared)
		done <- err
	}()

	// One of the two writes is denied by its own policy even though the
	// other runs concurrently. Both policies deny this path, so the point is
	// that neither call observes the other's policy object.
	require.Error(t, <-done)
	require.Error(t, <-done)
}

// runPersistent executes a command with the given policy and reports the
// result, so concurrent calls cannot share state.
func runPersistent(t *testing.T, policy *sandbox.Policy, cwd, command string) (string, string, error) {
	t.Helper()
	var stdout, stderr bytes.Buffer
	err := Run(context.Background(), RunOptions{
		Command: command,
		Cwd:     cwd,
		Env:     os.Environ(),
		Stdout:  &stdout,
		Stderr:  &stderr,
		Policy:  policy,
	})
	return stdout.String(), stderr.String(), err
}

// sprintf is a tiny local helper so the table above reads cleanly.
func sprintf(format, arg string) string {
	return format[:len(format)-2] + arg
}

// testBackgroundShellManager returns an isolated manager so tests do not
// share global job state.
func testBackgroundShellManager(t *testing.T) *BackgroundShellManager {
	t.Helper()
	return newBackgroundShellManager()
}
