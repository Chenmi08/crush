//go:build linux

package sandbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"runtime"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// bubblewrapTestPolicy builds a policy with a workspace, a private temp
// directory, and a cache directory, all under one temp root.
func bubblewrapTestPolicy(t *testing.T) *Policy {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	cache := filepath.Join(root, "cache")
	require.NoError(t, os.MkdirAll(cache, 0o755))
	policy, err := Resolve(Options{
		Workspace: workspace,
		TempDir:   filepath.Join(root, "tmp"),
		CacheDir:  cache,
	})
	require.NoError(t, err)
	return policy
}

// requireBubblewrap skips unless the runner is functionally usable. The skip
// reason names the dependency, as the PRD requires.
func requireBubblewrap(t *testing.T) {
	t.Helper()
	if runtime.GOOS != "linux" {
		t.Skip("bubblewrap backend is Linux-only")
	}
	backend := &bubblewrapBackend{}
	if err := backend.Available(t.Context()); err != nil {
		t.Skipf("bubblewrap unavailable: %v", err)
	}
}

// runWrapped executes command under the backend and returns stdout, stderr,
// and the error.
func runWrapped(t *testing.T, policy *Policy, dir, shellCommand string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("sh", "-c", shellCommand)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	cleanup, err := WrapCommand(t.Context(), policy, cmd)
	require.NoError(t, err)
	runErr := cmd.Run()
	if cleanup != nil {
		cleanup()
	}
	return stdout.String(), stderr.String(), runErr
}

// TestBubblewrap_WritesInsideRootsSucceed verifies a wrapped process can write
// inside the writable roots.
func TestBubblewrap_WritesInsideRootsSucceed(t *testing.T) {
	requireBubblewrap(t)
	policy := bubblewrapTestPolicy(t)

	inside := filepath.Join(policy.Workspace, "file.txt")
	_, stderr, err := runWrapped(t, policy, policy.Workspace, "echo hello > "+inside)
	require.NoError(t, err, "stderr=%q", stderr)

	content, err := os.ReadFile(inside)
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(content))

	cacheFile := filepath.Join(policy.CacheDir, "cache.txt")
	_, stderr, err = runWrapped(t, policy, policy.Workspace, "echo cached > "+cacheFile)
	require.NoError(t, err, "stderr=%q", stderr)
}

// TestBubblewrap_WritesOutsideRootsFail verifies the kernel refuses a write
// outside the roots.
func TestBubblewrap_WritesOutsideRootsFail(t *testing.T) {
	requireBubblewrap(t)
	policy := bubblewrapTestPolicy(t)

	outside := filepath.Join(t.TempDir(), "escape.txt")
	_, stderr, err := runWrapped(t, policy, policy.Workspace, "echo x > "+outside)
	require.Error(t, err, "a confined write outside the roots must fail")
	require.True(t, isReadOnlyFilesystemError(stderr), "stderr should report a read-only filesystem: %q", stderr)

	_, statErr := os.Stat(outside)
	require.True(t, os.IsNotExist(statErr), "the file must not be created")
}

// TestBubblewrap_GrandchildrenInheritBoundary verifies the boundary is
// inherited: a shell started by the confined process is confined too.
func TestBubblewrap_GrandchildrenInheritBoundary(t *testing.T) {
	requireBubblewrap(t)
	policy := bubblewrapTestPolicy(t)

	outside := filepath.Join(t.TempDir(), "grandchild.txt")
	_, stderr, err := runWrapped(t, policy, policy.Workspace,
		"sh -c 'sh -c \"echo x > "+outside+"\"'")
	require.Error(t, err)
	require.True(t, isReadOnlyFilesystemError(stderr), "stderr=%q", stderr)
	_, statErr := os.Stat(outside)
	require.True(t, os.IsNotExist(statErr))

	// A grandchild inside the workspace still works.
	inside := filepath.Join(policy.Workspace, "grandchild.txt")
	_, stderr, err = runWrapped(t, policy, policy.Workspace,
		"sh -c 'sh -c \"echo ok > "+inside+"\"'")
	require.NoError(t, err, "stderr=%q", stderr)
}

// TestBubblewrap_ProtectedPathRefused verifies a protected path inside a
// writable root is remounted read-only after the writable mount.
func TestBubblewrap_ProtectedPathRefused(t *testing.T) {
	requireBubblewrap(t)
	policy := bubblewrapTestPolicy(t)

	// The .git directory does not exist yet: the file itself must still be
	// refused, and the parent must not be creatable through the write.
	_, stderr, err := runWrapped(t, policy, policy.Workspace,
		"echo x > "+filepath.Join(policy.Workspace, "crushrc"))
	require.Error(t, err)
	require.True(t, isReadOnlyFilesystemError(stderr), "stderr=%q", stderr)
	_, statErr := os.Stat(filepath.Join(policy.Workspace, "crushrc"))
	require.True(t, os.IsNotExist(statErr))
}

// TestBubblewrap_TempDirRedirected verifies TMPDIR points at the private temp
// directory, so tools that write temp files neither fail nor leak into /tmp.
func TestBubblewrap_TempDirRedirected(t *testing.T) {
	requireBubblewrap(t)
	policy := bubblewrapTestPolicy(t)

	stdout, stderr, err := runWrapped(t, policy, policy.Workspace, "printf %s \"$TMPDIR\"")
	require.NoError(t, err, "stderr=%q", stderr)
	require.Equal(t, policy.TempDir, strings.TrimSpace(stdout))

	_, stderr, err = runWrapped(t, policy, policy.Workspace, "echo x > \"$TMPDIR/scratch\"")
	require.NoError(t, err, "stderr=%q", stderr)
	_, statErr := os.Stat(filepath.Join(policy.TempDir, "scratch"))
	require.NoError(t, statErr)
}

// TestBubblewrap_FailClosedWithoutRunner verifies that a confined mode with no
// usable backend refuses to run rather than running unconfined.
func TestBubblewrap_FailClosedWithoutRunner(t *testing.T) {
	ResetSelectionForTest()
	t.Cleanup(ResetSelectionForTest)

	// Override the probe by selecting an unavailable backend through the
	// package seam used by the runtime.
	forceSelection(t, Selection{
		Enforcement: EnforcementNone,
		Reason:      "bubblewrap is not installed",
	})

	policy := bubblewrapTestPolicy(t)
	cmd := exec.Command("sh", "-c", "echo hi")
	_, err := WrapCommand(t.Context(), policy, cmd)
	require.Error(t, err)
	require.True(t, IsRunnerError(err))
	require.Contains(t, err.Error(), "sandbox-unavailable")
	require.Contains(t, err.Error(), runtime.GOOS)
	require.Contains(t, err.Error(), "danger-full-access",
		"the failure must name the way out")
}

// TestBubblewrap_DangerFullAccessIsNotWrapped verifies the explicit opt-out.
func TestBubblewrap_DangerFullAccessIsNotWrapped(t *testing.T) {
	requireBubblewrap(t)
	policy := bubblewrapTestPolicy(t)
	policy.Mode = ModeDangerFullAccess

	cmd := exec.Command("sh", "-c", "true")
	before := cmd.Path
	_, err := WrapCommand(t.Context(), policy, cmd)
	require.NoError(t, err)
	require.Equal(t, before, cmd.Path)
}

// TestBubblewrap_ClassificationDistinguishesRunnerFailure verifies a runner
// failure and a policy denial are different error types: the model must not
// escalate around a broken sandbox.
func TestBubblewrap_ClassificationDistinguishesRunnerFailure(t *testing.T) {
	policy := bubblewrapTestPolicy(t)
	denial := policy.CheckWrite(filepath.Join(t.TempDir(), "x"))
	require.True(t, IsDenial(denial))
	require.False(t, IsRunnerError(denial))

	runnerErr := &RunnerError{Backend: "bubblewrap", Platform: "linux", Detail: "boom"}
	require.True(t, IsRunnerError(runnerErr))
	require.False(t, IsDenial(runnerErr))
	require.Contains(t, runnerErr.Error(), "sandbox-unavailable")
	require.Contains(t, runnerErr.Error(), "backend=bubblewrap")

	// A runner failure is never a denial, and a denial is never a runner
	// failure: the model must not escalate around infrastructure failure.
	wrapped := &RunnerError{Backend: "bubblewrap", Platform: "linux", Err: denial}
	require.True(t, IsRunnerError(wrapped))
	require.False(t, IsDenial(wrapped), "the outer classification wins")
}

// isReadOnlyFilesystemError reports whether the command output describes the
// kernel's read-only filesystem refusal. The exact wording varies by util
// (coreutils, dash), so several forms are accepted; the point is that the
// backend, not the policy layer, produced it.
func isReadOnlyFilesystemError(stderr string) bool {
	for _, needle := range []string{
		"Read-only file system",
		"read-only file system",
		"Permission denied",
		"Operation not permitted",
	} {
		if strings.Contains(stderr, needle) {
			return true
		}
	}
	return false
}

// TestBubblewrap_ProtectedVariantsRefused verifies every category the PRD
// lists is refused by the kernel boundary, not just the one representative
// path covered above.
func TestBubblewrap_ProtectedVariantsRefused(t *testing.T) {
	requireBubblewrap(t)
	policy := bubblewrapTestPolicy(t)

	targets := []string{
		filepath.Join(policy.Workspace, "crushrc"),
		filepath.Join(policy.Workspace, "AGENTS.md"),
		filepath.Join(policy.Workspace, "CLAUDE.local.md"),
		filepath.Join(policy.Workspace, ".crush"),
		filepath.Join(policy.Workspace, ".git", "config"),
		filepath.Join(policy.Workspace, ".git", "hooks", "pre-commit"),
		filepath.Join(policy.Workspace, ".bashrc"),
	}
	for _, target := range targets {
		_, stderr, err := runWrapped(t, policy, policy.Workspace, "echo x > "+target)
		require.Error(t, err, target)
		_, statErr := os.Stat(target)
		require.True(t, os.IsNotExist(statErr), "%s must not be created", target)
		require.NotEmpty(t, stderr, target)
	}
}

// TestBubblewrap_GitObjectsStayWritable verifies protection is targeted: git's
// ordinary operation is not broken by protecting its execution surfaces.
func TestBubblewrap_GitObjectsStayWritable(t *testing.T) {
	requireBubblewrap(t)
	// A workspace that already is a repository: with no .git, the whole
	// path is protected (nothing in it could execute yet), which is a
	// different guarantee.
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, ".git", "objects"), 0o755))
	policy, err := Resolve(Options{
		Workspace: workspace,
		TempDir:   filepath.Join(root, "tmp"),
		CacheDir:  filepath.Join(root, "cache"),
	})
	require.NoError(t, err)

	target := filepath.Join(policy.Workspace, ".git", "objects", "ab", "cdef")
	_, stderr, runErr := runWrapped(t, policy, policy.Workspace,
		"mkdir -p "+filepath.Dir(target)+" && echo x > "+target)
	require.NoError(t, runErr, "stderr=%q", stderr)
	_, statErr := os.Stat(target)
	require.NoError(t, statErr)
}

// TestBubblewrap_NoArtifactsLeftBehind verifies a refused write leaves no
// placeholder or mount point in the workspace.
func TestBubblewrap_NoArtifactsLeftBehind(t *testing.T) {
	requireBubblewrap(t)
	policy := bubblewrapTestPolicy(t)

	_, _, err := runWrapped(t, policy, policy.Workspace,
		"echo x > "+filepath.Join(policy.Workspace, "crushrc"))
	require.Error(t, err)

	entries, err := os.ReadDir(policy.Workspace)
	require.NoError(t, err)
	require.Empty(t, entries, "a refused write must leave no filesystem trace")
}

// TestBubblewrap_ProtectedGitInNonRepository verifies a workspace without a
// repository cannot have .git/hooks created: the whole .git path is masked,
// so a later `git init && git commit` has nothing of the agent's to execute.
func TestBubblewrap_ProtectedGitInNonRepository(t *testing.T) {
	requireBubblewrap(t)
	policy := bubblewrapTestPolicy(t)
	require.NoDirExists(t, filepath.Join(policy.Workspace, ".git"))

	_, stderr, err := runWrapped(t, policy, policy.Workspace,
		"mkdir -p .git/hooks && echo 'echo pwned' > .git/hooks/pre-commit")
	require.Error(t, err, "stderr=%q", stderr)
	_, statErr := os.Stat(filepath.Join(policy.Workspace, ".git"))
	require.True(t, os.IsNotExist(statErr), ".git must not be creatable inside the sandbox")
}

// TestBubblewrap_WorktreeGitFile verifies a worktree (.git is a file) is not
// broken by the protected-path mounts: the mount points must never include a
// path below the regular .git file.
func TestBubblewrap_WorktreeGitFile(t *testing.T) {
	requireBubblewrap(t)
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	gitdir := filepath.Join(root, "gitdirs", "feature")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.MkdirAll(filepath.Join(gitdir, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: "+gitdir+"\n"), 0o644))

	policy, err := Resolve(Options{
		Workspace: workspace,
		TempDir:   filepath.Join(root, "tmp"),
		CacheDir:  filepath.Join(root, "cache"),
	})
	require.NoError(t, err)

	// The runner must start at all: a mount over .git/config (below a
	// regular file) made every command fail with "Not a directory".
	stdout, stderr, runErr := runWrapped(t, policy, policy.Workspace, "echo ok")
	require.NoError(t, runErr, "stderr=%q", stderr)
	require.Equal(t, "ok\n", stdout)

	// The resolved gitdir's hooks stay protected.
	_, stderr, runErr = runWrapped(t, policy, policy.Workspace,
		"echo x > "+filepath.Join(gitdir, "hooks", "pre-commit"))
	require.Error(t, runErr, "stderr=%q", stderr)
}

// TestBubblewrap_HonoursWorkingDir verifies the runner starts in the command's
// directory, so `working_dir` and an in-interpreter `cd` reach child
// processes.
func TestBubblewrap_HonoursWorkingDir(t *testing.T) {
	requireBubblewrap(t)
	policy := bubblewrapTestPolicy(t)
	sub := filepath.Join(policy.Workspace, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	stdout, stderr, err := runWrapped(t, policy, sub, "pwd")
	require.NoError(t, err, "stderr=%q", stderr)
	require.Equal(t, sub, strings.TrimSpace(stdout))
}
