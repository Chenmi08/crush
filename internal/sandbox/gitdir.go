package sandbox

import "strings"

// parseGitdir extracts the target of a `.git` file (worktrees and
// submodules), whose content is a single `gitdir: <path>` line. Relative
// targets are resolved against the directory holding the .git file, per git's
// own semantics.
func parseGitdir(content string) (string, bool) {
	for _, line := range strings.Split(content, "\n") {
		line = strings.TrimSpace(line)
		if !strings.HasPrefix(line, "gitdir:") {
			continue
		}
		target := strings.TrimSpace(strings.TrimPrefix(line, "gitdir:"))
		if target == "" {
			return "", false
		}
		return target, true
	}
	return "", false
}
