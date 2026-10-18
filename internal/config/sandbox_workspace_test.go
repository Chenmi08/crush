package config

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestWorkspaceConfigCannotWidenSandbox pins the fix for a fail-open path: the
// machine-owned workspace state file is merged after the tighten-only layer
// check, and with the documented `--data-dir .crush` layout it lives inside
// the workspace the agent can write. Without a clamp, a file the agent wrote
// could set danger-full-access.
func TestWorkspaceConfigCannotWidenSandbox(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "crush"))

	workDir := t.TempDir()
	dataDir := filepath.Join(workDir, ".crush")
	store, err := Load(workDir, dataDir, false)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dataDir, "crush.json"),
		[]byte(`{"permissions":{"sandbox":{"mode":"danger-full-access"}}}`),
		0o600,
	))

	require.NoError(t, store.ReloadFromDisk(t.Context()))
	rt := store.ResolveSandboxRuntime()
	require.NoError(t, rt.Err)
	require.NotEqual(t, "danger-full-access", rt.RequestedMode,
		"a file inside the workspace must not disable the sandbox")
	require.NotNil(t, rt.Policy)
}

// TestWorkspaceConfigCannotAddWriteRoot verifies the other widening key.
func TestWorkspaceConfigCannotAddWriteRoot(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "crush"))

	workDir := t.TempDir()
	dataDir := filepath.Join(workDir, ".crush")
	store, err := Load(workDir, dataDir, false)
	require.NoError(t, err)

	extra := t.TempDir()
	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dataDir, "crush.json"),
		[]byte(`{"permissions":{"sandbox":{"allow_write":["`+extra+`"]}}}`),
		0o600,
	))

	require.NoError(t, store.ReloadFromDisk(t.Context()))
	rt := store.ResolveSandboxRuntime()
	require.NoError(t, rt.Err)
	require.NotNil(t, rt.Policy)
	for _, root := range rt.Policy.WriteRoots {
		require.NotEqual(t, extra, root, "the workspace config must not grant a writable root")
	}
}

// TestWorkspaceConfigMayTighten verifies the clamp keeps the legitimate use:
// narrowing the mode and adding denials.
func TestWorkspaceConfigMayTighten(t *testing.T) {
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "crush"))

	workDir := t.TempDir()
	dataDir := filepath.Join(workDir, ".crush")
	store, err := Load(workDir, dataDir, false)
	require.NoError(t, err)

	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	require.NoError(t, os.WriteFile(
		filepath.Join(dataDir, "crush.json"),
		[]byte(`{"permissions":{"sandbox":{"mode":"read-only","deny_write":["./secrets"]}}}`),
		0o600,
	))

	require.NoError(t, store.ReloadFromDisk(t.Context()))
	box := store.Config().Permissions.SandboxConfig()
	require.Equal(t, "read-only", box.Mode)
	require.Equal(t, []string{"./secrets"}, box.DenyWrite)
}
