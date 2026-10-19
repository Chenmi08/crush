package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// testPolicy builds a resolved policy rooted at a fresh temp workspace with a
// sibling temp directory and cache directory, all outside the workspace.
func testPolicy(t *testing.T) *Policy {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "workspace")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	tempDir := filepath.Join(root, "tmp")
	cacheDir := filepath.Join(root, "cache")
	require.NoError(t, os.MkdirAll(cacheDir, 0o755))

	p, err := Resolve(Options{
		Workspace:       workspace,
		TempDir:         tempDir,
		CacheDir:        cacheDir,
		GlobalConfigDir: filepath.Join(root, "config"),
		GlobalDataDir:   filepath.Join(root, "data"),
	})
	require.NoError(t, err)
	return p
}

func TestSubagentPolicyFor(t *testing.T) {
	t.Parallel()

	t.Run("main agents keep the workspace policy", func(t *testing.T) {
		t.Parallel()
		p := testPolicy(t)
		require.Same(t, p, SubagentPolicyFor(false, p))
	})

	t.Run("sub-agents are narrowed to read-only", func(t *testing.T) {
		t.Parallel()
		p := testPolicy(t)
		sub := SubagentPolicyFor(true, p)

		require.Equal(t, ModeReadOnly, sub.Mode)
		// The workspace's own policy object is untouched.
		require.Equal(t, ModeWorkspaceWrite, p.Mode)

		// Workspace and cache are no longer writable.
		require.Error(t, sub.CheckWrite(filepath.Join(p.Workspace, "file.go")))
		require.Error(t, sub.CheckWrite(filepath.Join(p.CacheDir, "x")))

		// The private temp directory stays writable so spill files and
		// scratch output still work.
		require.NoError(t, sub.CheckWrite(filepath.Join(p.TempDir, "spill.txt")))

		// Protected paths stay protected.
		require.NotEmpty(t, sub.Protected)
		require.Equal(t, p.Protected, sub.Protected)
	})

	t.Run("a danger-full-access parent still yields a confined sub-agent", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		workspace := filepath.Join(root, "ws")
		require.NoError(t, os.MkdirAll(workspace, 0o755))
		p, err := Resolve(Options{
			Workspace: workspace,
			TempDir:   filepath.Join(root, "tmp"),
			Config:    Config{Mode: ModeDangerFullAccess},
		})
		require.NoError(t, err)

		sub := SubagentPolicyFor(true, p)
		require.Equal(t, ModeReadOnly, sub.Mode)
		require.Error(t, sub.CheckWrite(filepath.Join(workspace, "file.go")),
			"sub-agents must not inherit the parent's full access")
	})

	t.Run("a parent without a temp directory grants no writes", func(t *testing.T) {
		t.Parallel()
		root := t.TempDir()
		workspace := filepath.Join(root, "ws")
		require.NoError(t, os.MkdirAll(workspace, 0o755))
		p, err := Resolve(Options{Workspace: workspace, Config: Config{Mode: ModeWorkspaceWrite}})
		require.NoError(t, err)
		require.Empty(t, p.TempDir)

		sub := SubagentPolicy(p)
		require.Empty(t, sub.WriteRoots)
		require.Error(t, sub.CheckWrite(filepath.Join(workspace, "file.go")))
	})

	t.Run("a nil parent yields nil", func(t *testing.T) {
		t.Parallel()
		require.Nil(t, SubagentPolicy(nil))
		require.Nil(t, SubagentPolicyFor(true, nil))
	})
}

func TestCheckRead_TempDirOnly(t *testing.T) {
	t.Parallel()
	p := testPolicy(t)

	require.True(t, p.CheckRead(filepath.Join(p.TempDir, "spill.txt")),
		"the session's own spill files read without prompting")
	require.True(t, p.CheckRead(filepath.Join(p.TempDir, "nested", "out.md")),
		"reads inside the temp directory are covered at any depth")
	require.False(t, p.CheckRead(filepath.Join(p.Workspace, "file.go")),
		"CheckRead is not a general read exemption")
	require.False(t, p.CheckRead(p.TempDir+"-sibling/file"),
		"a sibling directory sharing a prefix is not inside the temp directory")

	// A nil policy leaves the caller's behavior in place.
	var nilPolicy *Policy
	require.False(t, nilPolicy.CheckRead("/any/path"))

	// A policy with no temp directory grants no read exemption.
	noTemp := &Policy{Mode: ModeWorkspaceWrite}
	require.False(t, noTemp.CheckRead("/any/path"))
}

func TestResolve_DefaultModeAndRoots(t *testing.T) {
	t.Parallel()
	p := testPolicy(t)

	require.Equal(t, ModeWorkspaceWrite, p.Mode, "workspace-write is the default")
	require.Contains(t, p.WriteRoots, p.Workspace)
	require.Contains(t, p.WriteRoots, p.TempDir, "the private temp directory is writable")
	require.Contains(t, p.WriteRoots, p.CacheDir, "the user cache directory is writable")
}

func TestResolve_Modes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	cache := filepath.Join(root, "cache")
	require.NoError(t, os.MkdirAll(cache, 0o755))
	temp := filepath.Join(root, "tmp")

	t.Run("read-only grants only the private temp directory", func(t *testing.T) {
		t.Parallel()
		p, err := Resolve(Options{
			Workspace: workspace, TempDir: temp, CacheDir: cache,
			Config: Config{Mode: ModeReadOnly},
		})
		require.NoError(t, err)
		require.Equal(t, []string{CanonicalPath(temp)}, p.WriteRoots)
		require.NoError(t, p.CheckWrite(filepath.Join(temp, "scratch")))
		require.Error(t, p.CheckWrite(filepath.Join(workspace, "file")))
	})

	t.Run("danger-full-access bypasses the boundary", func(t *testing.T) {
		t.Parallel()
		p, err := Resolve(Options{Workspace: workspace, Config: Config{Mode: ModeDangerFullAccess}})
		require.NoError(t, err)
		require.NoError(t, p.CheckWrite("/etc/passwd"))
	})

	t.Run("invalid mode is rejected", func(t *testing.T) {
		t.Parallel()
		_, err := Resolve(Options{Workspace: workspace, Config: Config{Mode: "yolo"}})
		require.Error(t, err)
		require.Contains(t, err.Error(), "accepted")
	})
}

func TestCheckWrite_Containment(t *testing.T) {
	t.Parallel()
	p := testPolicy(t)
	outside := t.TempDir()

	// Inside the workspace.
	require.NoError(t, p.CheckWrite(filepath.Join(p.Workspace, "a", "b.txt")))
	// The workspace itself.
	require.NoError(t, p.CheckWrite(p.Workspace))
	// Outside every root.
	require.Error(t, p.CheckWrite(filepath.Join(outside, "file.txt")))
	// Dot-dot traversal out of the workspace.
	require.Error(t, p.CheckWrite(filepath.Join(p.Workspace, "..", "escape.txt")))
	// A sibling whose name shares the workspace prefix is not inside it.
	require.Error(t, p.CheckWrite(p.Workspace+"-sibling/file.txt"))
}

func TestCheckWrite_DeniesAndGrantsPrecedence(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "secrets", "nested"), 0o755))

	p, err := Resolve(Options{
		Workspace: workspace,
		Config: Config{
			Mode:       ModeWorkspaceWrite,
			AllowWrite: []string{filepath.Join(root, "extra")},
			DenyWrite:  []string{filepath.Join(workspace, "secrets")},
		},
	})
	require.Error(t, err, "a non-existent allow-write root must be reported")

	require.NoError(t, os.MkdirAll(filepath.Join(root, "extra"), 0o755))
	p, err = Resolve(Options{
		Workspace: workspace,
		Config: Config{
			Mode:       ModeWorkspaceWrite,
			AllowWrite: []string{filepath.Join(root, "extra")},
			DenyWrite:  []string{filepath.Join(workspace, "secrets")},
		},
	})
	require.NoError(t, err)

	require.NoError(t, p.CheckWrite(filepath.Join(root, "extra", "file")))
	require.Error(t, p.CheckWrite(filepath.Join(workspace, "secrets", "token")),
		"a denial inside a writable root wins over the grant")
	require.Error(t, p.CheckWrite(filepath.Join(workspace, "secrets", "nested", "deep", "file")))

	// A denial nested inside another denial is harmless: still denied.
	p, err = Resolve(Options{
		Workspace: workspace,
		Config: Config{
			Mode:      ModeWorkspaceWrite,
			DenyWrite: []string{filepath.Join(workspace, "secrets"), filepath.Join(workspace, "secrets", "nested")},
		},
	})
	require.NoError(t, err)
	require.Error(t, p.CheckWrite(filepath.Join(workspace, "secrets", "nested", "file")))
}

func TestCheckWrite_SymlinksResolved(t *testing.T) {
	t.Parallel()
	p := testPolicy(t)
	outside := t.TempDir()

	// A link inside the workspace pointing outside it must be refused.
	link := filepath.Join(p.Workspace, "escape")
	require.NoError(t, os.Symlink(outside, link))
	require.Error(t, p.CheckWrite(filepath.Join(link, "file.txt")))

	// A link inside the workspace pointing inside it is fine.
	inside := filepath.Join(p.Workspace, "real")
	require.NoError(t, os.MkdirAll(inside, 0o755))
	insideLink := filepath.Join(p.Workspace, "inside-link")
	require.NoError(t, os.Symlink(inside, insideLink))
	require.NoError(t, p.CheckWrite(filepath.Join(insideLink, "file.txt")))
}

func TestCheckWrite_Sinks(t *testing.T) {
	t.Parallel()
	p := testPolicy(t)
	require.NoError(t, p.CheckWrite("/dev/null"))
}

func TestCheckWrite_ProtectedCannotBeReallowed(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	workspace := filepath.Join(root, "ws")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	p, err := Resolve(Options{
		Workspace: workspace,
		Config: Config{
			Mode: ModeWorkspaceWrite,
			// A grant cannot override protection.
			AllowWrite: []string{workspace},
		},
	})
	require.NoError(t, err)

	err = p.CheckWrite(filepath.Join(workspace, "crushrc"))
	require.Error(t, err)
	d, ok := AsDenial(err)
	require.True(t, ok)
	require.True(t, d.Protected)
	require.Contains(t, err.Error(), Marker)
	require.Contains(t, err.Error(), filepath.Join(workspace, "crushrc"))

	for _, rel := range []string{".git/hooks/pre-commit", ".git/config", ".git/config.lock"} {
		require.Error(t, p.CheckWrite(filepath.Join(workspace, rel)), rel)
	}
}

func TestDenial_MarkerIsStable(t *testing.T) {
	t.Parallel()
	p := testPolicy(t)
	outside := t.TempDir()

	err := p.CheckWrite(filepath.Join(outside, "x"))
	require.Equal(t, Marker, "sandbox-denied")
	require.Contains(t, err.Error(), "sandbox-denied mode=workspace-write path=")
	require.Contains(t, err.Error(), "remedy=")
	require.True(t, IsDenial(err))
}

func TestDescribe_ReportsEnforcementForSelectedBackend(t *testing.T) {
	t.Parallel()
	p := testPolicy(t)
	status := Describe(t.Context(), p)

	sel := Select(t.Context())
	require.Equal(t, sel.Enforcement, status.Enforcement,
		"the reported enforcement must be the selected backend's")
	require.Contains(t, status.Note, status.Enforcement.Note())
	require.Contains(t, status.String(), "sandbox=workspace-write")
	if status.Enforcement == EnforcementPartial {
		require.NotEmpty(t, status.Missing)
	}
}

// TestCanonicalProtectedPaths_SortedAndDeduped pins the shape of the
// protected set: sorted input-independent output with no duplicates, which is
// what makes generated backend rules stable in golden files.
func TestCanonicalProtectedPaths_SortedAndDeduped(t *testing.T) {
	t.Parallel()
	dir := t.TempDir()
	a := filepath.Join(dir, "a")
	b := filepath.Join(dir, "b")
	out := CanonicalProtectedPaths([]string{b, a, b, ""})
	require.Equal(t, []string{a, b}, out)
}

// sandboxResetDenialCountForTest resets the process-wide refusal counter so
// assertions are deterministic.
func sandboxResetDenialCountForTest() { denialCount.Store(0) }

// TestDenialCountSurfacesInMessage verifies that repeated refusals tell the
// model it is going in circles, while the first refusals stay clean.
func TestDenialCountSurfacesInMessage(t *testing.T) {
	p := testPolicy(t)
	outside := t.TempDir()

	// Reset the process-wide counter so the assertion is deterministic.
	sandboxResetDenialCountForTest()

	first := p.CheckWrite(filepath.Join(outside, "a")).Error()
	require.NotContains(t, first, "refusal", "the first refusal carries no nudge")

	// The second refusal is still quiet; from the third on, the message
	// nudges the model to stop and explain rather than keep retrying.
	_ = p.CheckWrite(filepath.Join(outside, "b")).Error()
	second := p.CheckWrite(filepath.Join(outside, "b")).Error()
	require.Contains(t, second, "refusal 3 this session")

	third := p.CheckWrite(filepath.Join(outside, "c")).Error()
	require.Contains(t, third, "refusal 4 this session")
	require.Contains(t, third, "stop and explain")
}
