package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// CrushConfigNames are the Crush configuration files that grant code
// execution and therefore must never be writable by the agent.
var CrushConfigNames = []string{
	"crushrc",
	".crushrc",
	"crush.json",
	".crush.json",
}

// ShellStartupNames are shell startup files, protected only when they fall
// inside a writable root (a dotfiles repository, for example).
var ShellStartupNames = []string{
	".bashrc",
	".bash_profile",
	".bash_login",
	".profile",
	".zshrc",
	".zshenv",
	".zprofile",
	".zlogin",
}

// DefaultProtectedPaths returns the protected paths for a workspace. The list
// is defined here and consumed by the in-process fence and every backend; no
// backend carries its own copy.
//
// globalDirs are Crush's configuration and data directories. They are passed
// in rather than looked up here so this package stays free of the config
// package (which would create an import cycle through the shell).
func DefaultProtectedPaths(workspace string, globalDirs ...string) []string {
	var paths []string
	workspace = CanonicalPath(workspace)

	// Crush configuration and state that live in the global directories.
	for _, dir := range globalDirs {
		if dir == "" || dir == "." {
			continue
		}
		for _, name := range CrushConfigNames {
			paths = append(paths, filepath.Join(dir, name))
		}
	}

	if workspace == "" {
		return CanonicalProtectedPaths(paths)
	}

	// Configuration and state inside the workspace.
	for _, name := range CrushConfigNames {
		paths = append(paths, filepath.Join(workspace, name))
	}
	// The whole .crush directory is protected: it holds project state and a
	// nested crushrc would be executed on reload.
	paths = append(paths, filepath.Join(workspace, ".crush"))

	// Git's execution surfaces. .git may be a file (worktrees, submodules)
	// pointing at the real gitdir; include both the apparent and resolved
	// locations.
	paths = append(paths, gitProtectedPaths(workspace)...)

	// Shell startup files, only when they sit inside the workspace (a
	// dotfiles repository).
	for _, name := range ShellStartupNames {
		paths = append(paths, filepath.Join(workspace, name))
	}

	return CanonicalProtectedPaths(paths)
}

// gitProtectedPaths returns the protected .git paths for workspace.
//
// The governing repository is resolved by walking up from the workspace, the
// way git itself does. The workspace is often a subdirectory of a checkout (a
// monorepo package, for example), where `workspace/.git` does not exist at
// all. Treating that as "no repository yet" invented a protected path for a
// repository that was already there, and made every command run in the
// subdirectory fail: the backends materialize a mount point for a masked path
// that does not exist, and the empty file they used to leave behind is not a
// valid gitfile, so `git status` stopped with "invalid gitfile format"
// instead of continuing to the repository above it.
//
// Two things are protected in that case:
//
//   - `workspace/.git`, so no nested repository (and no hooks directory a
//     later `git init` would adopt) can be created inside the workspace.
//     It is masked as an empty directory, which git skips as "not a
//     repository" and keeps walking up from.
//   - The execution surfaces of the repository that actually governs the
//     workspace. They normally live outside every writable root, where the
//     read-only root mount already denies writes; naming them anyway keeps
//     the in-process fence honest when a wider allow_write grant covers
//     them.
//
// The shapes of .git are handled separately because a path below a regular
// file cannot be mounted:
//
//   - Directory (an ordinary checkout): hooks, config, config.lock.
//   - File (a worktree or submodule): the file itself, so the agent cannot
//     repoint the repository, plus the resolved gitdir's execution surfaces.
//   - Absent everywhere: .git itself, stricter than denying the individual
//     files, and the reason `git init` needs a wider mode when confined.
func gitProtectedPaths(workspace string) []string {
	own := filepath.Join(workspace, ".git")

	git, found := findEnclosingGit(workspace)
	if !found {
		return []string{own}
	}
	if git == own {
		return gitExecutionSurfaces(git)
	}

	// The workspace sits inside a repository, so its own .git path stays
	// protected as a whole while git finds the governing one above it.
	return append([]string{own}, gitExecutionSurfaces(git)...)
}

// gitExecutionSurfaces returns the paths through which a repository's .git
// entry can execute code later. The entry may be a directory (an ordinary
// checkout) or a file (a worktree or submodule) pointing at the real gitdir.
func gitExecutionSurfaces(git string) []string {
	info, err := os.Lstat(git)
	if err != nil {
		return nil
	}
	if info.IsDir() {
		return gitProtectedFor(git)
	}

	// .git is a file containing "gitdir: <path>". Relative targets are
	// resolved against the directory holding the file, per git's semantics.
	// The file itself is protected so it cannot be repointed.
	paths := []string{git}
	data, err := os.ReadFile(git)
	if err != nil {
		return paths
	}
	target, ok := parseGitdir(string(data))
	if !ok {
		return paths
	}
	if !filepath.IsAbs(target) {
		target = filepath.Join(filepath.Dir(git), target)
	}
	paths = append(paths, gitProtectedFor(target)...)

	// A linked worktree's gitdir holds only the worktree-private files
	// (HEAD, index, refs); hooks and config live in the common directory it
	// names, so they are the execution surfaces that matter.
	if common, ok := commonGitDir(target); ok {
		paths = append(paths, gitProtectedFor(common)...)
	}
	return paths
}

// commonGitDir resolves the shared repository directory a linked worktree's
// gitdir points at. It reads the `commondir` file, whose content is relative
// to the worktree's gitdir, per git's own semantics.
func commonGitDir(gitDir string) (string, bool) {
	data, err := os.ReadFile(filepath.Join(gitDir, "commondir"))
	if err != nil {
		return "", false
	}
	common := strings.TrimSpace(string(data))
	if common == "" {
		return "", false
	}
	if !filepath.IsAbs(common) {
		common = filepath.Join(gitDir, common)
	}
	return filepath.Clean(common), true
}

// findEnclosingGit walks up from dir looking for a `.git` entry, returning
// its path and whether one was found. The entry may be a directory (an
// ordinary checkout) or a file (a worktree or submodule); callers decide
// which execution surfaces to protect.
//
// The walk stops at the filesystem root. A `.git` that exists but cannot be
// inspected still counts as found: the repository exists, and treating it as
// absent would invent a protected path where a real one belongs.
func findEnclosingGit(dir string) (string, bool) {
	for {
		git := filepath.Join(dir, ".git")
		if _, err := os.Lstat(git); err == nil {
			return git, true
		}

		parent := filepath.Dir(dir)
		if parent == dir {
			return "", false
		}
		dir = parent
	}
}

func gitProtectedFor(gitPath string) []string {
	return []string{
		filepath.Join(gitPath, "hooks"),
		filepath.Join(gitPath, "config"),
		filepath.Join(gitPath, "config.lock"),
	}
}

// CanonicalProtectedPaths canonicalizes and deduplicates protected paths,
// dropping empty entries.
func CanonicalProtectedPaths(paths []string) []string {
	out := make([]string, 0, len(paths))
	seen := make(map[string]bool, len(paths))
	for _, p := range paths {
		if p == "" {
			continue
		}
		canonical := CanonicalPath(p)
		if canonical == "" || seen[canonical] {
			continue
		}
		seen[canonical] = true
		out = append(out, canonical)
	}
	slices.Sort(out)
	return out
}
