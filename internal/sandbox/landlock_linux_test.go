//go:build linux

package sandbox

import (
	"bytes"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
	"golang.org/x/sys/unix"
)

// requireLandlock skips unless the kernel exposes Landlock and the helper can
// apply a ruleset. The skip reason names the dependency.
func requireLandlock(t *testing.T) {
	t.Helper()
	backend := &landlockBackend{}
	if err := backend.Available(t.Context()); err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}
}

// landlockTestPolicy builds a policy with a workspace, a private temp
// directory, and a cache directory under one temp root.
func landlockTestPolicy(t *testing.T) (*Policy, string) {
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
	return policy, root
}

// runLandlock executes command through the helper, without going through the
// cached backend selection. The executable path is redirected to the test
// binary, whose TestMain acts as the helper.
func runLandlock(t *testing.T, policy *Policy, dir, shellCommand string) (string, string, error) {
	t.Helper()
	exe, err := os.Executable()
	require.NoError(t, err)
	previous := ExecutablePath
	ExecutablePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { ExecutablePath = previous })

	cmd := exec.Command("/bin/sh", "-c", shellCommand)
	cmd.Dir = dir
	cmd.Env = os.Environ()
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	backend := &landlockBackend{}
	cleanup, err := backend.Wrap(t.Context(), policy, cmd)
	require.NoError(t, err)
	runErr := cmd.Run()
	if cleanup != nil {
		cleanup()
	}
	return stdout.String(), stderr.String(), runErr
}

// TestLandlock_Confinement verifies writes inside the roots succeed and writes
// outside fail.
func TestLandlock_Confinement(t *testing.T) {
	requireLandlock(t)
	policy, _ := landlockTestPolicy(t)

	inside := filepath.Join(policy.Workspace, "file.txt")
	_, stderr, err := runLandlock(t, policy, policy.Workspace, "echo hello > "+inside)
	require.NoError(t, err, "stderr=%q", stderr)
	content, err := os.ReadFile(inside)
	require.NoError(t, err)
	require.Equal(t, "hello\n", string(content))

	outside := filepath.Join(t.TempDir(), "escape.txt")
	_, stderr, err = runLandlock(t, policy, policy.Workspace, "echo x > "+outside)
	require.Error(t, err, "a confined write outside the roots must fail")
	require.True(t, strings.Contains(stderr, "Permission denied") ||
		strings.Contains(stderr, "Read-only file system") ||
		strings.Contains(stderr, "Operation not permitted"), "stderr=%q", stderr)
	_, statErr := os.Stat(outside)
	require.True(t, os.IsNotExist(statErr))
}

// TestLandlock_GrandchildrenInheritBoundary verifies the boundary survives a
// nested shell.
func TestLandlock_GrandchildrenInheritBoundary(t *testing.T) {
	requireLandlock(t)
	policy, _ := landlockTestPolicy(t)

	outside := filepath.Join(t.TempDir(), "grandchild.txt")
	_, _, err := runLandlock(t, policy, policy.Workspace,
		"sh -c 'sh -c \"echo x > "+outside+"\"'")
	require.Error(t, err)
	_, statErr := os.Stat(outside)
	require.True(t, os.IsNotExist(statErr))
}

// TestLandlock_SinksAllowed verifies /dev/null stays writable.
func TestLandlock_SinksAllowed(t *testing.T) {
	requireLandlock(t)
	policy, _ := landlockTestPolicy(t)

	_, stderr, err := runLandlock(t, policy, policy.Workspace, "echo x > /dev/null")
	require.NoError(t, err, "stderr=%q", stderr)
}

// TestLandlock_CrushProcessNotRestricted verifies the restriction is applied in
// the helper, not in the test (that is, Crush) process: the current process can
// still write anywhere after a confined command ran.
func TestLandlock_CrushProcessNotRestricted(t *testing.T) {
	requireLandlock(t)
	policy, _ := landlockTestPolicy(t)

	_, _, _ = runLandlock(t, policy, policy.Workspace, "echo x > /dev/null")

	outside := filepath.Join(t.TempDir(), "still-writable.txt")
	require.NoError(t, os.WriteFile(outside, []byte("x"), 0o644),
		"the Crush process must never be restricted")
}

// TestLandlock_PolicyTravelsOnDescriptor verifies the policy never appears in
// argv or the environment: both are readable by the confined process.
func TestLandlock_PolicyTravelsOnDescriptor(t *testing.T) {
	policy, root := landlockTestPolicy(t)

	exe, err := os.Executable()
	require.NoError(t, err)
	previous := ExecutablePath
	ExecutablePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { ExecutablePath = previous })

	cmd := exec.Command("/bin/true")
	backend := &landlockBackend{}
	cleanup, err := backend.Wrap(t.Context(), policy, cmd)
	require.NoError(t, err)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}

	require.Contains(t, cmd.Env, PolicyFDEnv+"=3")
	require.NotEmpty(t, cmd.ExtraFiles, "the policy must travel on an inherited descriptor")
	joined := strings.Join(cmd.Args, " ") + " " + strings.Join(cmd.Env, " ")
	require.NotContains(t, joined, root,
		"the policy must not travel through argv or the environment")
	require.NotContains(t, joined, string(policy.Mode)+" "+policy.Workspace)
}

// TestLandlock_PartialReporting verifies the backend reports partial when the
// policy needs subtraction, and that the status names the missing guarantee.
func TestLandlock_PartialReporting(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := protectedPolicy(t, workspace)
	backend := &landlockBackend{}

	require.True(t, policy.RequiresSubtraction())
	require.Equal(t, EnforcementPartial, backend.Enforcement(policy))

	status := Describe(t.Context(), &Policy{Mode: ModeWorkspaceWrite})
	if status.Backend == "landlock" {
		require.Equal(t, EnforcementPartial, status.Enforcement)
	}
}

// TestLandlock_FullWhenNoSubtraction verifies enforcement is reported full only
// when the policy needs no subtraction and the kernel exposes the classes.
func TestLandlock_FullWhenNoSubtraction(t *testing.T) {
	requireLandlock(t)
	policy := &Policy{
		Mode:       ModeWorkspaceWrite,
		Workspace:  t.TempDir(),
		WriteRoots: nil,
	}
	require.False(t, policy.RequiresSubtraction())
	require.Equal(t, EnforcementFull, (&landlockBackend{}).Enforcement(policy))

	// A kernel with a narrower ABI is partial, not full: it must not claim
	// classes it does not implement.
	narrow := handledAccess(landlockABIRefer)
	require.Zero(t, narrow&unix.LANDLOCK_ACCESS_FS_TRUNCATE,
		"an ABI below 3 must not claim the truncate class")
	require.NotZero(t, handledAccess(landlockABITruncate)&unix.LANDLOCK_ACCESS_FS_TRUNCATE)
	require.Zero(t, handledAccess(1)&unix.LANDLOCK_ACCESS_FS_REFER,
		"an ABI below 2 must not claim the refer class")
}

// TestLandlock_ABIVersion verifies the ABI probe returns a usable version and
// that a kernel without Landlock is reported as unavailable, not full.
func TestLandlock_ABIVersion(t *testing.T) {
	abi, err := landlockABI()
	if err != nil {
		t.Skipf("landlock unavailable: %v", err)
	}
	require.Positive(t, abi)
	require.NotZero(t, handledAccess(abi))
}

// TestLandlock_UnavailableIsFailClosed verifies a kernel without Landlock
// produces the same fail-closed refusal as a missing bubblewrap.
func TestLandlock_UnavailableIsFailClosed(t *testing.T) {
	err := landlockUnavailable(os.ErrNotExist)
	require.Contains(t, err.Error(), "bubblewrap")
	require.Contains(t, err.Error(), "danger-full-access")
	require.Contains(t, err.Error(), "landlock")
}

// TestLandlock_HelperRequiresTarget verifies a helper invocation without a
// target command fails with the helper's status rather than panicking.
func TestLandlock_HelperRequiresTarget(t *testing.T) {
	t.Setenv(HelperEnv, "1")
	t.Setenv(PolicyFDEnv, "")
	handled, code := RunLandlockHelper(nil)
	require.True(t, handled)
	require.NotZero(t, code)
}

// TestLandlock_HelperArgvNamesSubcommand verifies the re-exec path passes the
// hidden subcommand token. Without it the Crush binary treats the target as a
// command of its own and the fallback backend is unusable.
func TestLandlock_HelperArgvNamesSubcommand(t *testing.T) {
	policy, _ := landlockTestPolicy(t)

	exe, err := os.Executable()
	require.NoError(t, err)
	previous := ExecutablePath
	ExecutablePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { ExecutablePath = previous })

	cmd := exec.Command("/bin/echo", "hello")
	backend := &landlockBackend{}
	cleanup, err := backend.Wrap(t.Context(), policy, cmd)
	require.NoError(t, err)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}

	require.Equal(t, exe, cmd.Path)
	require.GreaterOrEqual(t, len(cmd.Args), 3)
	require.Equal(t, HelperSubcommand, cmd.Args[1], "the helper must be invoked as a subcommand")
	require.Equal(t, "/bin/echo", cmd.Args[2], "the target command follows the subcommand token")
	require.Equal(t, "hello", cmd.Args[3])
}

// TestLandlock_HelperKeepsCommandEnv verifies the command's own environment is
// preserved and wins over the parent's: appending os.Environ() after it let
// the parent override exports set on the command.
func TestLandlock_HelperKeepsCommandEnv(t *testing.T) {
	policy, _ := landlockTestPolicy(t)

	exe, err := os.Executable()
	require.NoError(t, err)
	previous := ExecutablePath
	ExecutablePath = func() (string, error) { return exe, nil }
	t.Cleanup(func() { ExecutablePath = previous })

	cmd := exec.Command("/bin/true")
	cmd.Env = []string{"PATH=/custom/bin", "FOO=command"}
	backend := &landlockBackend{}
	cleanup, err := backend.Wrap(t.Context(), policy, cmd)
	require.NoError(t, err)
	if cleanup != nil {
		t.Cleanup(cleanup)
	}

	require.Equal(t, []string{"PATH=/custom/bin", "FOO=command"}, cmd.Env[:2],
		"the command's environment must come first and stay intact")
	require.Contains(t, cmd.Env, HelperEnv+"=1")
	require.Contains(t, cmd.Env, PolicyFDEnv+"=3")
}
