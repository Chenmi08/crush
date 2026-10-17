//go:build darwin

package sandbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// requireSeatbelt skips unless sandbox-exec can functionally apply a profile.
// The skip reason names the dependency.
func requireSeatbelt(t *testing.T) {
	t.Helper()
	if err := (&seatbeltBackend{}).Available(t.Context()); err != nil {
		t.Skipf("seatbelt unavailable: %v", err)
	}
}

// seatbeltTestPolicyForBehavior builds a policy with a workspace, cache, and
// private temp directory under one temp root.
func seatbeltTestPolicyForBehavior(t *testing.T) *Policy {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	cache := filepath.Join(root, "cache")
	for _, dir := range []string{workspace, cache} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}
	policy, err := Resolve(Options{
		Workspace: workspace,
		TempDir:   filepath.Join(root, "tmp"),
		CacheDir:  cache,
	})
	require.NoError(t, err)
	return policy
}

// runSeatbelt executes command under the backend.
func runSeatbelt(t *testing.T, policy *Policy, dir, shellCommand string) (string, string, error) {
	t.Helper()
	cmd := exec.Command("/bin/sh", "-c", shellCommand)
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

// TestSeatbelt_WritesInsideRootsSucceed verifies a wrapped process can write
// inside the writable roots.
func TestSeatbelt_WritesInsideRootsSucceed(t *testing.T) {
	requireSeatbelt(t)
	policy := seatbeltTestPolicyForBehavior(t)

	inside := filepath.Join(policy.Workspace, "file.txt")
	_, stderr, err := runSeatbelt(t, policy, policy.Workspace, "echo hello > "+inside)
	require.NoError(t, err, "stderr=%q", stderr)
	content, err := os.ReadFile(inside)
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(content))
}

// TestSeatbelt_WritesOutsideRootsFail verifies the operation-not-permitted
// refusal outside the roots.
func TestSeatbelt_WritesOutsideRootsFail(t *testing.T) {
	requireSeatbelt(t)
	policy := seatbeltTestPolicyForBehavior(t)

	outside := filepath.Join(t.TempDir(), "escape.txt")
	_, stderr, err := runSeatbelt(t, policy, policy.Workspace, "echo x > "+outside)
	require.Error(t, err, "a confined write outside the roots must fail")
	require.True(t,
		strings.Contains(stderr, "Operation not permitted") ||
			strings.Contains(stderr, "Permission denied"),
		"stderr should report the kernel refusal: %q", stderr)
	_, statErr := os.Stat(outside)
	require.True(t, os.IsNotExist(statErr))
}

// TestSeatbelt_ProtectedPathRefused verifies a protected path inside a
// writable root is denied by the trailing deny rule.
func TestSeatbelt_ProtectedPathRefused(t *testing.T) {
	requireSeatbelt(t)
	policy := seatbeltTestPolicyForBehavior(t)

	target := filepath.Join(policy.Workspace, "crushrc")
	_, _, err := runSeatbelt(t, policy, policy.Workspace, "echo x > "+target)
	require.Error(t, err)
	_, statErr := os.Stat(target)
	require.True(t, os.IsNotExist(statErr), "the protected path must not be created")
}

// TestSeatbelt_ReadsUnconfined verifies reads outside the workspace work.
func TestSeatbelt_ReadsUnconfined(t *testing.T) {
	requireSeatbelt(t)
	policy := seatbeltTestPolicyForBehavior(t)

	outside := filepath.Join(t.TempDir(), "readable.txt")
	require.NoError(t, os.WriteFile(outside, []byte("readable\n"), 0o644))
	stdout, stderr, err := runSeatbelt(t, policy, policy.Workspace, "cat "+outside)
	require.NoError(t, err, "stderr=%q", stderr)
	require.Equal(t, "readable\n", stdout)
}

// TestSeatbelt_FailClosed verifies a runner failure is distinct from a policy
// denial and refuses the command.
func TestSeatbelt_FailClosed(t *testing.T) {
	ResetSelectionForTest()
	t.Cleanup(ResetSelectionForTest)
	forceSelection(t, Selection{
		Enforcement: EnforcementNone,
		Reason:      "/usr/bin/sandbox-exec is not available",
	})

	policy := seatbeltTestPolicyForBehavior(t)
	cmd := exec.Command("/bin/sh", "-c", "true")
	_, err := WrapCommand(t.Context(), policy, cmd)
	require.Error(t, err)
	require.True(t, IsRunnerError(err))
	require.False(t, IsDenial(err))
	require.Contains(t, err.Error(), "danger-full-access")
}
