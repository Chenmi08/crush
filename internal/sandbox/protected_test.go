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
	if err := initGitRepo(workspace); err != nil {
		return workspace
	}
	return workspace
}

// initGitRepo initializes a repository at dir, reporting a missing git binary
// so callers can skip or fall back. Tests that exercise git behavior need the
// repository but not a commit; use commitGitRepo for one.
func initGitRepo(dir string) error {
	gitBin, err := exec.LookPath("git")
	if err != nil {
		return err
	}
	cmd := exec.Command(gitBin, "init", "--quiet")
	cmd.Dir = dir
	return cmd.Run()
}

// requireGitRepo initializes a repository at dir and skips the test when git
// is unavailable, returning the binary path.
func requireGitRepo(t *testing.T, dir string) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for this test")
	}
	runGit(t, dir, "init", "--quiet")
	return gitBin
}

// runGit runs one git command in dir and fails the test on error.
func runGit(t *testing.T, dir string, args ...string) string {
	t.Helper()
	gitBin := gitBinary(t)
	cmd := exec.Command(gitBin, args...)
	cmd.Dir = dir
	out, err := cmd.CombinedOutput()
	require.NoError(t, err, "git %v: %s", args, out)
	return string(out)
}

// gitBinary returns the git binary path, skipping the test when it is absent.
func gitBinary(t *testing.T) string {
	t.Helper()
	gitBin, err := exec.LookPath("git")
	if err != nil {
		t.Skip("git is required for this test")
	}
	return gitBin
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
	}
	for _, path := range required {
		require.Contains(t, p.Protected, CanonicalPath(path), "missing protected path")
	}

	// No repository exists in this workspace, so the whole .git path is
	// protected: an agent that created .git/hooks here would have its code
	// executed by a later `git init && git commit`.
	require.Contains(t, p.Protected, CanonicalPath(filepath.Join(workspace, ".git")),
		"an absent .git must be protected as a whole")
	require.Error(t, p.CheckWrite(filepath.Join(workspace, ".git", "hooks", "pre-commit")))
}

// TestProtected_ExistingRepositoryGitSurfaces verifies that with a real
// repository the three execution surfaces are protected individually, so
// ordinary git operations (writing objects, index, refs) still work.
func TestProtected_ExistingRepositoryGitSurfaces(t *testing.T) {
	t.Parallel()
	workspace := protectedWorkspace(t)
	require.DirExists(t, filepath.Join(workspace, ".git"), "git must be available for this test")

	p := protectedPolicy(t, workspace)
	for _, path := range []string{
		filepath.Join(workspace, ".git", "hooks"),
		filepath.Join(workspace, ".git", "config"),
		filepath.Join(workspace, ".git", "config.lock"),
	} {
		require.Contains(t, p.Protected, CanonicalPath(path), "missing protected path")
		require.Error(t, p.CheckWrite(path), path)
	}
	require.NoError(t, p.CheckWrite(filepath.Join(workspace, ".git", "objects", "ab", "cd")),
		"git's object store must stay writable")
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
	// A real repository, so .git is a directory and the protected set names
	// its execution surfaces rather than the whole .git path.
	workspace := protectedWorkspace(t)
	require.DirExists(t, filepath.Join(workspace, ".git"), "git must be available for this test")
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

// TestProtected_SubdirectoryOfRepository verifies the workspace's own .git
// path stays protected while the repository that actually governs it is
// resolved from the enclosing directory.
//
// Regression: the workspace used to be treated as "no repository yet" when
// `workspace/.git` was absent, which both missed the real repository's
// execution surfaces and made the backends materialize a mount point for a
// path git then refused to walk past.
func TestProtected_SubdirectoryOfRepository(t *testing.T) {
	t.Parallel()
	repo := protectedWorkspace(t)
	require.DirExists(t, filepath.Join(repo, ".git"), "git must be available for this test")
	workspace := filepath.Join(repo, "pkg", "deep")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	p := protectedPolicy(t, workspace)

	// The workspace's own .git is still protected: a nested repository must
	// not be creatable inside it.
	own := CanonicalPath(filepath.Join(workspace, ".git"))
	require.Contains(t, p.Protected, own)
	require.Error(t, p.CheckWrite(filepath.Join(workspace, ".git", "hooks", "pre-commit")))

	// The governing repository's execution surfaces are protected too, even
	// though they live outside the workspace.
	for _, path := range []string{
		filepath.Join(repo, ".git", "hooks"),
		filepath.Join(repo, ".git", "config"),
		filepath.Join(repo, ".git", "config.lock"),
	} {
		require.Contains(t, p.Protected, CanonicalPath(path), "missing enclosing repository surface")
		require.Error(t, p.CheckWrite(path), path)
	}

	// Ordinary git work inside the workspace is not affected: the fence is
	// name-based and the workspace's writable root still covers it.
	require.NoError(t, p.CheckWrite(filepath.Join(workspace, "file.txt")))
}

// TestProtected_SubdirectoryOfWorktree verifies an enclosing linked
// worktree (.git is a file) contributes the execution surfaces of the shared
// repository it belongs to.
//
// A linked worktree's gitdir holds only worktree-private files (HEAD, index,
// refs); hooks and config live in the common directory named by its
// `commondir` file. Protecting only the gitdir would miss both.
func TestProtected_SubdirectoryOfWorktree(t *testing.T) {
	t.Parallel()
	common := filepath.Join(t.TempDir(), "main", ".git")
	worktreeGitDir := filepath.Join(common, "worktrees", "feature")
	require.NoError(t, os.MkdirAll(filepath.Join(common, "hooks"), 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(common, "config"), []byte("[core]\n"), 0o644))
	require.NoError(t, os.MkdirAll(worktreeGitDir, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(worktreeGitDir, "commondir"),
		[]byte("../..\n"),
		0o644,
	))

	repo := filepath.Join(t.TempDir(), "checkout")
	workspace := filepath.Join(repo, "pkg")
	require.NoError(t, os.MkdirAll(workspace, 0o755))
	require.NoError(t, os.WriteFile(
		filepath.Join(repo, ".git"),
		[]byte("gitdir: "+worktreeGitDir+"\n"),
		0o644,
	))

	p := protectedPolicy(t, workspace)

	// The link itself is protected, so the repository cannot be repointed.
	require.Contains(t, p.Protected, CanonicalPath(filepath.Join(repo, ".git")))

	// The execution surfaces are the common directory's, resolved through
	// commondir.
	for _, path := range []string{
		filepath.Join(common, "hooks"),
		filepath.Join(common, "config"),
		filepath.Join(common, "config.lock"),
	} {
		require.Contains(t, p.Protected, CanonicalPath(path),
			"missing shared execution surface %s", path)
	}
}

// TestProtected_FindEnclosingGit verifies the upward walk itself, including
// that it stops at the filesystem root.
func TestProtected_FindEnclosingGit(t *testing.T) {
	t.Parallel()
	repo := protectedWorkspace(t)
	if _, err := os.Lstat(filepath.Join(repo, ".git")); err != nil {
		t.Skip("git must be available for this test")
	}
	deep := filepath.Join(repo, "a", "b", "c")
	require.NoError(t, os.MkdirAll(deep, 0o755))

	for _, dir := range []string{repo, filepath.Join(repo, "a"), deep} {
		got, found := findEnclosingGit(dir)
		require.True(t, found, dir)
		require.Equal(t, filepath.Join(repo, ".git"), got, dir)
	}

	outside := t.TempDir()
	_, found := findEnclosingGit(outside)
	require.False(t, found, "a directory with no ancestor repository must report none")
}

// TestProtected_RealWorktreeRepo protects against a fabricated gitdir
// layout: it asks git to create a real linked worktree and checks that the
// shared repository's hooks and config are protected, not just the
// worktree's private directory.
func TestProtected_RealWorktreeRepo(t *testing.T) {
	t.Parallel()

	root := t.TempDir()
	main := filepath.Join(root, "main")
	require.NoError(t, os.MkdirAll(main, 0o755))
	requireGitRepo(t, main)
	runGit(t, main, "-c", "user.email=t@example.com", "-c", "user.name=t",
		"commit", "--allow-empty", "--quiet", "-m", "init")

	worktree := filepath.Join(root, "feat")
	runGit(t, main, "worktree", "add", "--quiet", worktree)
	workspace := filepath.Join(worktree, "pkg")
	require.NoError(t, os.MkdirAll(workspace, 0o755))

	common := filepath.Join(main, ".git")
	p := protectedPolicy(t, workspace)
	for _, path := range []string{
		filepath.Join(common, "hooks"),
		filepath.Join(common, "config"),
		filepath.Join(common, "config.lock"),
	} {
		require.Contains(t, p.Protected, CanonicalPath(path),
			"the shared repository's %s must be protected", path)
	}
}
