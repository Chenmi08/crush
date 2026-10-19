package sandbox

import (
	"os"
	"path/filepath"
	"slices"
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

// gitProtectedPaths returns the protected .git paths for workspace. The three
// shapes .git can take are handled separately, because a path below a regular
// file cannot be mounted:
//
//   - Directory (an ordinary checkout): hooks, config, config.lock.
//   - File (a worktree or submodule): the file itself, so the agent cannot
//     repoint the repository, plus the resolved gitdir's execution surfaces.
//   - Absent: .git itself, so the agent cannot create a hooks directory that a
//     later `git init && git commit` would execute. This is stricter than
//     denying the individual files, and it is why `git init` needs a wider
//     mode when the sandbox is confined.
func gitProtectedPaths(workspace string) []string {
	git := filepath.Join(workspace, ".git")

	info, err := os.Lstat(git)
	switch {
	case err == nil && info.IsDir():
		return gitProtectedFor(git)

	case err == nil:
		// .git is a file containing "gitdir: <path>". Relative targets are
		// resolved against the directory holding the file, per git's
		// semantics. Protect the link itself so it cannot be repointed.
		paths := []string{git}
		if data, err := os.ReadFile(git); err == nil {
			if target, ok := parseGitdir(string(data)); ok {
				if !filepath.IsAbs(target) {
					target = filepath.Join(workspace, target)
				}
				paths = append(paths, gitProtectedFor(target)...)
			}
		}
		return paths

	default:
		// No repository yet. Deny the whole .git path: any hooks directory
		// created here would execute on a later `git commit`.
		return []string{git}
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
