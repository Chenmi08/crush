package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/stretchr/testify/require"
)

// trustIsolatedEnv points HOME/XDG/CRUSH_* and the trust store at a throwaway
// directory. No t.Parallel(): these tests Setenv.
func trustIsolatedEnv(t *testing.T) (workDir, dataDir string) {
	t.Helper()
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(isolated, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "crush"))
	return t.TempDir(), t.TempDir()
}

func writeProjectCrushrc(t *testing.T, dir, body string) string {
	t.Helper()
	path := filepath.Join(dir, "crushrc")
	require.NoError(t, os.WriteFile(path, []byte(body), 0o644))
	return path
}

// TestLoad_UntrustedProjectConfigIsNotExecuted is the core trust contract: a
// freshly cloned repository's crushrc and crush.json must not run until the
// user says so, and declining must leave global configuration intact.
func TestLoad_UntrustedProjectConfigIsNotExecuted(t *testing.T) {
	workDir, dataDir := trustIsolatedEnv(t)

	writeProjectCrushrc(t, workDir, "option notifications bell\n")
	require.NoError(t, os.WriteFile(
		filepath.Join(workDir, "crush.json"),
		[]byte(`{"options": {"notifications": "osc"}}`),
		0o644,
	))

	// A user-level crushrc still applies: trust gates repository-local
	// configuration, not the user's own setup.
	globalDir := filepath.Dir(config.GlobalConfig())
	require.NoError(t, os.MkdirAll(globalDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(globalDir, "crushrc"),
		[]byte("option notifications native\n"),
		0o644,
	))

	store, err := config.Load(workDir, dataDir, false)
	require.NoError(t, err)

	require.Equal(t, "native", store.Config().Options.Notifications,
		"only the user-level config may apply while the project is untrusted")
	for _, path := range store.LoadedPaths() {
		require.NotEqual(t, filepath.Join(workDir, "crushrc"), path,
			"the untrusted project crushrc must not be loaded")
		require.NotEqual(t, filepath.Join(workDir, "crush.json"), path,
			"the untrusted project crush.json must not be loaded")
	}
}

// TestLoad_TrustedProjectConfigIsExecuted verifies that recording trust makes
// the project configuration run on the next load.
func TestLoad_TrustedProjectConfigIsExecuted(t *testing.T) {
	workDir, dataDir := trustIsolatedEnv(t)

	writeProjectCrushrc(t, workDir, "option notifications bell\n")
	config.TrustTestProject(t, workDir)

	store, err := config.Load(workDir, dataDir, false)
	require.NoError(t, err)
	require.Equal(t, "bell", store.Config().Options.Notifications)
}

// TestProjectTrust_RevocationStopsExecution verifies that withdrawing trust
// stops the project's configuration from running on the next load.
func TestProjectTrust_RevocationStopsExecution(t *testing.T) {
	workDir, dataDir := trustIsolatedEnv(t)
	writeProjectCrushrc(t, workDir, "option notifications bell\n")

	config.TrustTestProject(t, workDir)
	store, err := config.Load(workDir, dataDir, false)
	require.NoError(t, err)
	require.Equal(t, "bell", store.Config().Options.Notifications)

	require.NoError(t, config.RevokeProjectTrust(config.CanonicalProjectPath(workDir)))
	require.False(t, config.IsProjectTrusted(config.CanonicalProjectPath(workDir)))

	store, err = config.Load(workDir, dataDir, false)
	require.NoError(t, err)
	require.NotEqual(t, "bell", store.Config().Options.Notifications,
		"a revoked project must stop executing its config")
}

// TestProjectTrust_KeyedByCanonicalPath verifies the two properties of the
// trust key: separate checkouts are separate decisions, while a symlinked
// path resolves to the same decision.
func TestProjectTrust_KeyedByCanonicalPath(t *testing.T) {
	root, _ := trustIsolatedEnv(t)

	first := filepath.Join(root, "checkout-a")
	second := filepath.Join(root, "checkout-b")
	require.NoError(t, os.MkdirAll(first, 0o755))
	require.NoError(t, os.MkdirAll(second, 0o755))
	writeProjectCrushrc(t, first, "")

	config.TrustTestProject(t, first)

	require.True(t, config.IsProjectTrusted(config.CanonicalProjectPath(first)))
	require.False(t, config.IsProjectTrusted(config.CanonicalProjectPath(second)),
		"two checkouts of the same repository are separate decisions")

	link := filepath.Join(root, "checkout-link")
	require.NoError(t, os.Symlink(first, link))
	require.True(t, config.IsProjectTrusted(config.CanonicalProjectPath(link)),
		"a symlinked path resolves to the same canonical project")

	require.True(t, config.CanonicalProjectPath(first) != config.CanonicalProjectPath(second))
}

// TestNewTrustPrompt_ListsFiles verifies the prompt names the files that
// would run, and that a project without repository-local config needs no
// decision.
func TestNewTrustPrompt_ListsFiles(t *testing.T) {
	workDir, _ := trustIsolatedEnv(t)

	_, ok := config.NewTrustPrompt(workDir)
	require.False(t, ok, "a project without repository-local config needs no decision")

	writeProjectCrushrc(t, workDir, "")
	prompt, ok := config.NewTrustPrompt(workDir)
	require.True(t, ok)
	require.Contains(t, prompt.Files, filepath.Join(workDir, "crushrc"))
	require.Contains(t, prompt.Message(), filepath.Join(workDir, "crushrc"))
}

// TestLoad_BrokenTrustStoreDeniesTrust verifies that an unreadable trust file
// fails closed rather than granting trust.
func TestLoad_BrokenTrustStoreDeniesTrust(t *testing.T) {
	workDir, _ := trustIsolatedEnv(t)
	trustPath := filepath.Join(filepath.Dir(config.GlobalConfigData()), "trusted-projects.json")
	require.NoError(t, os.MkdirAll(filepath.Dir(trustPath), 0o755))
	require.NoError(t, os.WriteFile(trustPath, []byte("not json"), 0o600))

	require.False(t, config.IsProjectTrusted(config.CanonicalProjectPath(workDir)))
}
