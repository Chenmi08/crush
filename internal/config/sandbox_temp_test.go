package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// TestDefaultSandboxTempDir_ShortEnough pins that the private temp directory —
// which is exported as TMPDIR to every confined command — fits the Unix socket
// path limit. The default per-project data directory is long enough that its
// sandbox/tmp subtree made every socket bind inside it fail with EINVAL.
func TestDefaultSandboxTempDir_ShortEnough(t *testing.T) {
	t.Parallel()
	long := filepath.Join(t.TempDir(),
		"deeply", "nested", "project", "data", "directory", "that", "goes", "on")
	dir := config.DefaultSandboxTempDir(long)
	require.NotEmpty(t, dir)
	require.LessOrEqual(t, len(dir), 60,
		"TMPDIR must leave room for a socket name under sun_path (108 bytes)")

	// Stable across calls, so the directory is reused rather than recreated
	// with a new name on every resolution.
	require.Equal(t, dir, config.DefaultSandboxTempDir(long))
	// Distinct per data directory, so two projects do not share temp state.
	require.NotEqual(t, dir, config.DefaultSandboxTempDir(long+"2"))
}

// TestDefaultSandboxTempDir_ShortDataDirStaysLocal verifies the normal case is
// unchanged: the directory lives under the data directory. t.TempDir() itself
// can be deep, so a shallow path is built under the system temp dir.
func TestDefaultSandboxTempDir_ShortDataDirStaysLocal(t *testing.T) {
	// Not parallel: TMPDIR is patched so the fallback location is short.
	shortTmp, err := os.MkdirTemp("/tmp", "sd")
	require.NoError(t, err, "a short base directory is required for this test")
	t.Cleanup(func() { _ = os.RemoveAll(shortTmp) })
	t.Setenv("TMPDIR", shortTmp)

	dataDir := filepath.Join(shortTmp, "d")
	require.Equal(t, filepath.Join(dataDir, "sandbox", "tmp"),
		config.DefaultSandboxTempDir(dataDir))
	require.Empty(t, config.DefaultSandboxTempDir(""))
}

// TestDefaultSandboxTempDir_Usable verifies the short directory can actually
// be created and used, rather than only being short.
func TestDefaultSandboxTempDir_Usable(t *testing.T) {
	t.Parallel()
	long := filepath.Join(t.TempDir(), "a", "b", "c", "d", "e", "f", "g")
	dir := config.DefaultSandboxTempDir(long)
	require.NoError(t, os.MkdirAll(dir, 0o700))
	t.Cleanup(func() { _ = os.RemoveAll(dir) })
	_, err := os.CreateTemp(dir, "probe-*")
	require.NoError(t, err)
}
