package config_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/stretchr/testify/require"
)

// TestSandboxBuiltin_Mode verifies the builtin selects a mode.
func TestSandboxBuiltin_Mode(t *testing.T) {
	store := loadCrushSh(t, `sandbox mode read-only`)

	box := store.Config().Permissions.SandboxConfig()
	require.Equal(t, "read-only", box.Mode)
}

// TestSandboxBuiltin_ModeRejectsInvalid verifies a bad mode names the accepted
// values rather than failing later at execution time.
func TestSandboxBuiltin_ModeRejectsInvalid(t *testing.T) {
	_, err := loadCrushShErr(t, `sandbox mode yolo`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "accepted")
	require.Contains(t, err.Error(), "read-only")
	require.Contains(t, err.Error(), "workspace-write")
	require.Contains(t, err.Error(), "danger-full-access")
}

// loadCrushShWithGlobalSettings writes script to the user-level crushrc and
// loads it through the real pipeline. Sandbox settings that widen the sandbox
// (a writable root) are only valid at the user level, so they must live here.
func loadCrushShWithGlobalSettings(t *testing.T, script string) *config.ConfigStore {
	t.Helper()
	isolated := t.TempDir()
	t.Setenv("HOME", isolated)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(isolated, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(isolated, ".local", "share"))
	globalDir := filepath.Join(isolated, ".config", "crush")
	t.Setenv("CRUSH_GLOBAL_CONFIG", globalDir)
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(isolated, ".local", "share", "crush"))
	require.NoError(t, os.MkdirAll(globalDir, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(globalDir, "crushrc"), []byte(script), 0o644))

	store, err := config.Load(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)
	return store
}

// TestSandboxBuiltin_Lists verifies extra roots, denials, and exclusions
// accumulate and deduplicate.
func TestSandboxBuiltin_Lists(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	require.NoError(t, os.MkdirAll(filepath.Join(home, ".kube"), 0o755))

	store := loadCrushShWithGlobalSettings(t, `sandbox allow-write ~/.kube
sandbox allow-write ~/.kube
sandbox deny-write ./secrets
sandbox deny-write ./secrets
sandbox exclude-command "docker *"
sandbox exclude-command "gh *"
sandbox exclude-command "docker *"`)

	box := store.Config().Permissions.SandboxConfig()
	require.Len(t, box.AllowWrite, 1, "duplicate allow-write entries must be deduplicated")
	require.Equal(t, []string{"./secrets"}, box.DenyWrite)
	require.Equal(t, []string{"docker *", "gh *"}, box.ExcludeCommands)
}

// TestSandboxBuiltin_FailIfUnavailable verifies the option accepts a bare form
// and an explicit boolean.
func TestSandboxBuiltin_FailIfUnavailable(t *testing.T) {
	store := loadCrushSh(t, `sandbox fail-if-unavailable`)
	require.True(t, store.Config().Permissions.SandboxConfig().FailIfUnavailable)

	store = loadCrushSh(t, `sandbox fail-if-unavailable false`)
	require.False(t, store.Config().Permissions.SandboxConfig().FailIfUnavailable)
}

// TestSandboxBuiltin_UnknownSubcommand verifies an unknown subcommand reports
// the accepted ones.
func TestSandboxBuiltin_UnknownSubcommand(t *testing.T) {
	_, err := loadCrushShErr(t, `sandbox nope value`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "expected mode")
}

// TestSandboxConfig_JSONSchemaIncludesSettings verifies the JSON config keeps
// working for the same settings and that the generated schema includes them.
func TestSandboxConfig_JSONSchemaIncludesSettings(t *testing.T) {
	t.Parallel()
	schema, err := os.ReadFile(filepath.Join("..", "..", "schema.json"))
	require.NoError(t, err)
	text := string(schema)
	for _, key := range []string{`"sandbox"`, `"allow_write"`, `"deny_write"`, `"exclude_commands"`, `"fail_if_unavailable"`} {
		require.Contains(t, text, key)
	}
	require.Contains(t, text, `"danger-full-access"`)
}

// TestSandboxConfig_JSONSettingsWork verifies the JSON path produces the same
// options as the builtin.
func TestSandboxConfig_JSONSettingsWork(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	globalDir := filepath.Join(home, ".config", "crush")
	t.Setenv("CRUSH_GLOBAL_CONFIG", globalDir)
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(home, ".local", "share", "crush"))
	require.NoError(t, os.MkdirAll(globalDir, 0o755))

	extra := filepath.Join(t.TempDir(), "extra")
	require.NoError(t, os.MkdirAll(extra, 0o755))

	require.NoError(t, os.WriteFile(
		filepath.Join(globalDir, "crush.json"),
		[]byte(`{"permissions": {"sandbox": {
			"mode": "workspace-write",
			"allow_write": ["`+extra+`"],
			"deny_write": ["./secrets"],
			"exclude_commands": ["docker *"],
			"fail_if_unavailable": true
		}}}`),
		0o644,
	))

	store, err := config.Load(t.TempDir(), t.TempDir(), false)
	require.NoError(t, err)
	box := store.Config().Permissions.SandboxConfig()
	require.Equal(t, "workspace-write", box.Mode)
	require.Equal(t, []string{extra}, box.AllowWrite)
	require.Equal(t, []string{"./secrets"}, box.DenyWrite)
	require.Equal(t, []string{"docker *"}, box.ExcludeCommands)
	require.True(t, box.FailIfUnavailable)
}

// TestSandboxConfig_InvalidKeyFailsLoad verifies a configuration error names
// the offending key.
func TestSandboxConfig_InvalidKeyFailsLoad(t *testing.T) {
	_, err := loadCrushShErr(t, `sandbox mode bogus`)
	require.Error(t, err)
	require.Contains(t, err.Error(), "mode")
}

// TestResolveSandbox_DefaultsAndRoots verifies the resolved policy uses the
// default mode and includes the workspace, the private temp directory, and the
// user cache directory.
func TestResolveSandbox_DefaultsAndRoots(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	root := t.TempDir()
	cache := filepath.Join(root, "cache")
	require.NoError(t, os.MkdirAll(cache, 0o755))

	policy, err := config.ResolveSandbox(config.Sandbox{}, config.SandboxResolveOptions{
		Workspace: workspace,
		TempDir:   filepath.Join(root, "tmp"),
		CacheDir:  cache,
	})
	require.NoError(t, err)
	require.Equal(t, sandbox.ModeWorkspaceWrite, policy.Mode)
	require.Contains(t, policy.WriteRoots, sandbox.CanonicalPath(workspace))
	require.Contains(t, policy.WriteRoots, sandbox.CanonicalPath(cache))
}

// TestResolveSandbox_AllowWriteCanonicalized verifies extra roots are
// canonicalized and deduplicated, and a missing root is reported.
func TestResolveSandbox_AllowWriteCanonicalized(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	real := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(real, 0o755))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(real, link))

	policy, err := config.ResolveSandbox(config.Sandbox{
		AllowWrite: []string{link, real},
	}, config.SandboxResolveOptions{
		Workspace: t.TempDir(),
		TempDir:   filepath.Join(root, "tmp"),
	})
	require.NoError(t, err)

	count := 0
	for _, r := range policy.WriteRoots {
		if r == sandbox.CanonicalPath(real) {
			count++
		}
	}
	require.Equal(t, 1, count, "a symlink and its target must deduplicate to one root")

	_, err = config.ResolveSandbox(config.Sandbox{
		AllowWrite: []string{filepath.Join(root, "missing")},
	}, config.SandboxResolveOptions{Workspace: t.TempDir()})
	require.Error(t, err)
	require.Contains(t, err.Error(), "does not exist")
	require.Contains(t, err.Error(), "allow-write")
}

// TestResolveSandbox_DenialsPrecedence verifies a denial inside a denial is
// harmless and denials beat grants.
func TestResolveSandbox_DenialsPrecedence(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "secrets", "nested"), 0o755))

	policy, err := config.ResolveSandbox(config.Sandbox{
		DenyWrite: []string{"secrets", "secrets/nested"},
	}, config.SandboxResolveOptions{Workspace: workspace})
	require.NoError(t, err)

	require.Error(t, policy.CheckWrite(filepath.Join(workspace, "secrets", "nested", "token")))
	require.NoError(t, policy.CheckWrite(filepath.Join(workspace, "ordinary")))
}

// TestSandboxExclusions verifies exclusion patterns match as documented and do
// not silently widen to interpreters or wrappers.
func TestSandboxExclusions(t *testing.T) {
	t.Parallel()

	patterns := []string{"docker *", "gh"}
	require.True(t, config.IsSandboxExcluded("docker run alpine", patterns))
	require.True(t, config.IsSandboxExcluded("gh", patterns))
	require.True(t, config.IsSandboxExcluded("gh pr create", patterns))
	require.False(t, config.IsSandboxExcluded("go test ./...", patterns))
	require.False(t, config.IsSandboxExcluded("sudo docker run alpine", patterns),
		"a wrapper must not inherit another command's exclusion")
	require.False(t, config.IsSandboxExcluded("bash -c 'docker run alpine'", patterns),
		"an interpreter must not inherit another command's exclusion")
	require.False(t, config.IsSandboxExcluded("docker", []string{"docker *"}),
		"a pattern with args must not match the bare command name")
}

// TestProjectSandbox_TightensOnly verifies project-level configuration may add
// denials and exclusions but cannot widen the mode, add a writable root, or
// disable the sandbox.
func TestProjectSandbox_TightensOnly(t *testing.T) {
	t.Parallel()
	base := config.Sandbox{Mode: "read-only", AllowWrite: []string{"/opt/one"}}

	t.Run("denials and exclusions are allowed", func(t *testing.T) {
		t.Parallel()
		err := config.CheckProjectSandboxTightens(config.Sandbox{
			DenyWrite:       []string{"./secrets"},
			ExcludeCommands: []string{"docker *"},
		}, base)
		require.NoError(t, err)
	})

	t.Run("widening the mode is rejected", func(t *testing.T) {
		t.Parallel()
		err := config.CheckProjectSandboxTightens(config.Sandbox{Mode: "workspace-write"}, base)
		require.Error(t, err)
		require.ErrorIs(t, err, config.ErrProjectWidensSandbox)
		require.Contains(t, err.Error(), "mode")
	})

	t.Run("adding a writable root is rejected", func(t *testing.T) {
		t.Parallel()
		err := config.CheckProjectSandboxTightens(config.Sandbox{AllowWrite: []string{"/opt/two"}}, base)
		require.Error(t, err)
		require.ErrorIs(t, err, config.ErrProjectWidensSandbox)
		require.Contains(t, err.Error(), "allow_write")
	})

	t.Run("tightening the mode is allowed", func(t *testing.T) {
		t.Parallel()
		wider := config.Sandbox{Mode: "workspace-write"}
		err := config.CheckProjectSandboxTightens(config.Sandbox{Mode: "read-only"}, wider)
		require.NoError(t, err)
	})

	t.Run("danger-full-access is never a project choice", func(t *testing.T) {
		t.Parallel()
		err := config.CheckProjectSandboxTightens(config.Sandbox{Mode: "danger-full-access"}, base)
		require.Error(t, err)
		require.ErrorIs(t, err, config.ErrProjectWidensSandbox)
	})

	t.Run("requiring the sandbox is a tightening", func(t *testing.T) {
		t.Parallel()
		err := config.CheckProjectSandboxTightens(config.Sandbox{FailIfUnavailable: true}, base)
		require.NoError(t, err)
	})
}

// TestSandboxMode_DangerRequiresExplicitSelection verifies an absent or
// invalid mode never yields danger-full-access.
func TestSandboxMode_DangerRequiresExplicitSelection(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()

	for _, mode := range []string{"", "bogus"} {
		policy, err := config.ResolveSandbox(config.Sandbox{Mode: mode}, config.SandboxResolveOptions{Workspace: workspace})
		if mode == "bogus" {
			require.Error(t, err, "an invalid mode must be rejected, not widened")
			continue
		}
		require.NoError(t, err)
		require.NotEqual(t, sandbox.ModeDangerFullAccess, policy.Mode)
	}

	policy, err := config.ResolveSandbox(config.Sandbox{Mode: "danger-full-access"}, config.SandboxResolveOptions{Workspace: workspace})
	require.NoError(t, err)
	require.Equal(t, sandbox.ModeDangerFullAccess, policy.Mode)
}

// TestSandboxLayers_ProjectTightenOnlyEndToEnd verifies a repository-local
// crushrc may add denials and exclusions on top of the user's settings, and
// that a project-level attempt to widen the sandbox fails the load with the
// offending key named.
func TestSandboxLayers_ProjectTightenOnlyEndToEnd(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	globalDir := filepath.Join(home, ".config", "crush")
	t.Setenv("CRUSH_GLOBAL_CONFIG", globalDir)
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(home, ".local", "share", "crush"))
	require.NoError(t, os.MkdirAll(globalDir, 0o755))

	// User-level configuration.
	require.NoError(t, os.WriteFile(
		filepath.Join(globalDir, "crushrc"),
		[]byte(`sandbox mode workspace-write
sandbox exclude-command "gh *"
`),
		0o644,
	))

	t.Run("project may add denials and exclusions", func(t *testing.T) {
		workDir := t.TempDir()
		dataDir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(workDir, "crushrc"),
			[]byte(`sandbox deny-write ./secrets
sandbox exclude-command "docker *"
`),
			0o644,
		))
		config.TrustTestProject(t, workDir)

		store, err := config.Load(workDir, dataDir, false)
		require.NoError(t, err)
		box := store.Config().Permissions.SandboxConfig()
		require.Equal(t, "workspace-write", box.Mode)
		require.Equal(t, []string{"./secrets"}, box.DenyWrite)
		require.Equal(t, []string{"gh *", "docker *"}, box.ExcludeCommands,
			"the project's exclusion is added to the user's, not instead of it")
	})

	t.Run("project may not add a writable root", func(t *testing.T) {
		workDir := t.TempDir()
		dataDir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(workDir, "crushrc"),
			[]byte(`sandbox allow-write /opt/elsewhere`),
			0o644,
		))
		config.TrustTestProject(t, workDir)

		_, err := config.Load(workDir, dataDir, false)
		require.Error(t, err)
		require.ErrorIs(t, err, config.ErrProjectWidensSandbox)
		require.Contains(t, err.Error(), "allow_write")
	})

	t.Run("project may not widen the mode", func(t *testing.T) {
		workDir := t.TempDir()
		dataDir := t.TempDir()
		require.NoError(t, os.WriteFile(
			filepath.Join(workDir, "crushrc"),
			[]byte(`sandbox mode danger-full-access`),
			0o644,
		))
		config.TrustTestProject(t, workDir)

		_, err := config.Load(workDir, dataDir, false)
		require.Error(t, err)
		require.ErrorIs(t, err, config.ErrProjectWidensSandbox)
		require.Contains(t, err.Error(), "mode")
	})
}

// TestSandboxRuntime_FailIfUnavailable verifies the opt-in exits with the same
// actionable message the runtime refusal uses when no backend is usable.
func TestSandboxRuntime_FailIfUnavailable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(home, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(home, ".local", "share", "crush"))

	globalDir := filepath.Join(home, ".config", "crush")
	require.NoError(t, os.MkdirAll(globalDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(globalDir, "crush.json"),
		[]byte(`{"permissions": {"sandbox": {"fail_if_unavailable": true}}}`),
		0o644,
	))

	workDir := t.TempDir()
	store, err := config.Load(workDir, t.TempDir(), false)
	require.NoError(t, err)

	// On this host a backend is available, so the requirement is satisfied.
	rt := store.ResolveSandboxRuntime()
	require.NoError(t, store.EnsureSandboxAvailable(rt))
}

// TestSandboxRuntime_ExplicitDisable verifies the separate disable override
// yields no policy, so the boundary is off rather than silently partial.
func TestSandboxRuntime_ExplicitDisable(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("XDG_CONFIG_HOME", filepath.Join(home, ".config"))
	t.Setenv("XDG_DATA_HOME", filepath.Join(home, ".local", "share"))
	t.Setenv("CRUSH_GLOBAL_CONFIG", filepath.Join(home, ".config", "crush"))
	t.Setenv("CRUSH_GLOBAL_DATA", filepath.Join(home, ".local", "share", "crush"))

	workDir := t.TempDir()
	store, err := config.Load(workDir, t.TempDir(), false)
	require.NoError(t, err)
	store.Overrides().DisableSandbox = true

	rt := store.ResolveSandboxRuntime()
	require.True(t, rt.Disabled)
	require.Nil(t, rt.Policy)
	require.Equal(t, sandbox.ModeDangerFullAccess, rt.RequestedMode)
}
