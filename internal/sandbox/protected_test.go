package sandbox

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// protectedWorkspace builds a workspace with a real git repository so git's
// execution surfaces exist on disk.
func protectedWorkspace(t *testing.T) string {
	t.Helper()
	workspace := t.TempDir()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return workspace
	}
	cmd := exec.Command(gitBin, "init", "--quiet")
	cmd.Dir = workspace
	require.NoError(t, cmd.Run())
	return workspace
}

// protectedPolicy resolves a policy for a workspace.
func protectedPolicy(t *testing.T, workspace string) *Policy {
	t.Helper()
	p, err := Resolve(Options{
		Workspace: workspace,
		TempDir:   filepath.Join(t.TempDir(), "tmp"),
	})
	require.NoError(t, err)
	return p
}

// TestProtected_ListedPaths verifies every category the PRD names is in the
// protected set.
func TestProtected_ListedPaths(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	p := protectedPolicy(t, workspace)

	required := []string{
		// Crush configuration and state.
		filepath.Join(workspace, "crushrc"),
		filepath.Join(workspace, ".crushrc"),
		filepath.Join(workspace, "crush.json"),
		filepath.Join(workspace, ".crush.json"),
		filepath.Join(workspace, ".crush"),
		// Instruction files and their local variants.
		filepath.Join(workspace, "AGENTS.md"),
		filepath.Join(workspace, "AGENTS.local.md"),
		filepath.Join(workspace, "CRUSH.md"),
		filepath.Join(workspace, "CRUSH.local.md"),
		filepath.Join(workspace, "CLAUDE.md"),
		filepath.Join(workspace, "CLAUDE.local.md"),
		filepath.Join(workspace, "GEMINI.md"),
		filepath.Join(workspace, "GEMINI.local.md"),
		// Git execution surfaces.
		filepath.Join(workspace, ".git", "hooks"),
		filepath.Join(workspace, ".git", "config"),
		filepath.Join(workspace, ".git", "config.lock"),
	}
	for _, path := range required {
		require.Contains(t, p.Protected, CanonicalPath(path), "missing protected path")
	}
}

// TestProtected_ShellStartupFiles verifies shell startup files inside a
// writable root are protected (the dotfiles-repository case) but that files
// outside the workspace are not silently added to a workspace policy.
func TestProtected_ShellStartupFiles(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	p := protectedPolicy(t, workspace)

	for _, name := range ShellStartupNames {
		require.Contains(t, p.Protected, CanonicalPath(filepath.Join(workspace, name)))
	}
}

// TestProtected_WorktreeGitdir verifies the resolved gitdir of a worktree is
// protected, not just the apparent .git file.
func TestProtected_WorktreeGitdir(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()

	// Simulate a worktree's .git file pointing at a gitdir elsewhere.
	gitdir := filepath.Join(t.TempDir(), "worktrees", "feature")
	require.NoError(t, os.MkdirAll(filepath.Join(gitdir, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(gitdir, "config"), []byte("[core]\n"), 0o644))
	require.NoError(t, os.WriteFile(
		filepath.Join(workspace, ".git"),
		[]byte("gitdir: "+gitdir+"\n"),
		0o644,
	))

	p := protectedPolicy(t, workspace)
	require.Contains(t, p.Protected, CanonicalPath(filepath.Join(gitdir, "hooks")),
		"the resolved gitdir's hooks must be protected")
	require.Contains(t, p.Protected, CanonicalPath(filepath.Join(gitdir, "config")))
	require.Contains(t, p.Protected, CanonicalPath(filepath.Join(gitdir, "config.lock")))

	// A write through the resolved gitdir is refused.
	err := p.CheckWrite(filepath.Join(gitdir, "hooks", "pre-commit"))
	require.Error(t, err)
	d, ok := AsDenial(err)
	require.True(t, ok)
	require.True(t, d.Protected)
}

// TestProtected_RelativeWorktreeGitdir verifies a relative gitdir target is
// resolved against the directory holding the .git file.
func TestProtected_RelativeWorktreeGitdir(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	require.NoError(t, os.MkdirAll(filepath.Join(workspace, "..", "gitdirs", "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(workspace, ".git"), []byte("gitdir: ../gitdirs\n"), 0o644))

	p := protectedPolicy(t, workspace)
	resolved := CanonicalPath(filepath.Join(workspace, "..", "gitdirs", "hooks"))
	require.Contains(t, p.Protected, resolved)
}

// TestProtected_CannotBeReallowed verifies no setting can re-allow a
// protected path: not a writable root, not a broader grant.
func TestProtected_CannotBeReallowed(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	extra := filepath.Join(t.TempDir(), "extra")
	require.NoError(t, os.MkdirAll(extra, 0o755))

	p, err := Resolve(Options{
		Workspace: workspace,
		TempDir:   filepath.Join(t.TempDir(), "tmp"),
		Config: Config{
			Mode:       ModeWorkspaceWrite,
			AllowWrite: []string{workspace, extra},
			DenyWrite:  nil,
		},
	})
	require.NoError(t, err)

	for _, path := range []string{
		filepath.Join(workspace, "crushrc"),
		filepath.Join(workspace, "AGENTS.md"),
		filepath.Join(workspace, ".git", "config"),
		filepath.Join(workspace, ".bashrc"),
	} {
		err := p.CheckWrite(path)
		require.Error(t, err, path)
		d, ok := AsDenial(err)
		require.True(t, ok)
		require.True(t, d.Protected, path)
		require.Contains(t, err.Error(), "reason=protected", path)
		// The remedy must explain that it is protected, not merely outside
		// the workspace.
		require.Contains(t, err.Error(), "cannot be made writable", path)
	}
}

// TestProtected_AgreesAcrossConsumers verifies the in-process fence and every
// backend consume the same set: the backend rules are generated from the
// policy's Protected list, so a path refused by the fence also appears in the
// generated rules.
func TestProtected_AgreesAcrossConsumers(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	p := protectedPolicy(t, workspace)

	probe := filepath.Join(workspace, "crushrc")
	require.Error(t, p.CheckWrite(probe), "the fence must refuse the path")

	// The set a backend consumes is the same list.
	require.Contains(t, p.Protected, CanonicalPath(probe))

	// Renaming a protected path's parent does not move it out from under the
	// protection. Two properties hold:
	//
	//   1. Moving a directory out of the workspace is a write outside the
	//      roots, so it is refused outright.
	//   2. The protected set is name-based on canonical paths, and Crush only
	//      executes known names at the workspace root, so renaming a parent
	//      inside the workspace cannot grant execution of a config file.
	outside := t.TempDir()
	require.Error(t, p.CheckWrite(filepath.Join(outside, "moved")),
		"moving a directory out of the workspace is a write outside the roots")

	// The parent of a protected path is itself only protected when it is the
	// workspace root: `.git` must stay writable for ordinary git operations.
	require.NoError(t, p.CheckWrite(filepath.Join(workspace, ".git", "objects", "ab", "cd")),
		"git's object store stays writable")
	require.Error(t, p.CheckWrite(filepath.Join(workspace, ".git", "hooks", "pre-commit")))

	// A protected name reappears protected at any location the policy names,
	// because the set is regenerated from the canonical workspace each time.
	renamedWorkspace := filepath.Join(t.TempDir(), "ws")
	require.NoError(t, os.MkdirAll(renamedWorkspace, 0o755))
	p2 := protectedPolicy(t, renamedWorkspace)
	require.Error(t, p2.CheckWrite(filepath.Join(renamedWorkspace, "crushrc")))
}

// TestProtected_OutsideRootsReportsPartial verifies a backend that cannot
// express subtraction reports partial rather than full.
func TestProtected_OutsideRootsReportsPartial(t *testing.T) {
	t.Parallel()
	p := protectedPolicy(t, t.TempDir())
	require.True(t, p.RequiresSubtraction(),
		"the default policy has protected paths inside the workspace, so a union-only backend reports partial")
	require.NotEmpty(t, p.ProtectedInsideRoots())

	status := Status{Policy: p, Backend: "landlock", Enforcement: EnforcementPartial}
	status.Missing = partialMissing(p, "landlock")
	require.Contains(t, status.Missing, "cannot be subtracted")
}

// TestProtected_NoDuplicates verifies the canonical set is deduplicated and
// sorted, which is what makes generated backend rules stable.
func TestProtected_NoDuplicates(t *testing.T) {
	t.Parallel()
	p := protectedPolicy(t, t.TempDir())
	seen := make(map[string]bool, len(p.Protected))
	for i, path := range p.Protected {
		require.False(t, seen[path], "duplicate protected path: %s", path)
		seen[path] = true
		if i > 0 {
			require.Less(t, p.Protected[i-1], path, "protected paths must be sorted")
		}
	}
}

// TestProtected_DisplayNamesPath verifies a denial names the path and explains
// that it is protected.
func TestProtected_DisplayNamesPath(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	p := protectedPolicy(t, workspace)
	path := filepath.Join(workspace, ".git", "hooks", "pre-push")

	err := p.CheckWrite(path)
	require.Error(t, err)
	msg := err.Error()
	require.Contains(t, msg, Marker)
	require.Contains(t, msg, path)
	require.Contains(t, msg, "reason=protected")
	require.False(t, strings.Contains(msg, "reason=outside-writable-roots"))
}
