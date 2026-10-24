package agent

import (
	"context"
	"fmt"
	"os/exec"
	"strings"
	"time"
)

const (
	// worktreeStatusTimeout bounds the git query. The manifest is appended
	// to a summary; a slow or huge repository must not delay compaction.
	worktreeStatusTimeout = 5 * time.Second
	// worktreeMaxFiles bounds the query's output so a pathological working
	// tree cannot overflow the manifest budget before it is measured.
	worktreeMaxFiles = 50
)

// manifestWorktreeFile is one path git reports as modified in the working
// tree, and the short word for what happened to it.
type manifestWorktreeFile struct {
	Path   string
	Status string
}

// worktreeDirtyFiles asks git which files in dir differ from HEAD. It exists
// because tool calls are not the only way a file changes: an agent that edits
// through a shell command (`sed -i`, a heredoc, a redirection) leaves no path
// in any tool call input, so a manifest built from tool calls alone would
// report an empty file list while the working tree is full of changes.
//
// The command string is never parsed for paths. shell syntax makes that
// unreliable in both directions, and git already knows the answer. A clean
// tree returns no files and no error; a directory that is not a repository,
// or a missing git binary, returns an error the caller treats as "unknown".
func worktreeDirtyFiles(ctx context.Context, dir string) ([]manifestWorktreeFile, error) {
	if dir == "" {
		return nil, fmt.Errorf("no working directory")
	}
	ctx, cancel := context.WithTimeout(ctx, worktreeStatusTimeout)
	defer cancel()

	var stdout, stderr strings.Builder
	cmd := exec.CommandContext(ctx, "git", "status", "--porcelain=v1", "-z", "--untracked-files=normal")
	cmd.Dir = dir
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		return nil, fmt.Errorf("git status: %w: %s", err, strings.TrimSpace(stderr.String()))
	}
	return parseWorktreeStatus(stdout.String(), worktreeMaxFiles), nil
}

// parseWorktreeStatus reads `git status --porcelain=v1 -z` output. Entries are
// NUL separated as `<XY> <path>`, and a rename or copy is followed by a second
// NUL-separated entry holding the original path, which is skipped: the manifest
// lists where a file is now.
//
// Only paths relative to the queried directory are produced, so nothing here
// can leak an absolute path into the summary.
func parseWorktreeStatus(out string, limit int) []manifestWorktreeFile {
	var files []manifestWorktreeFile
	entries := strings.Split(out, "\x00")
	for i := 0; i < len(entries); i++ {
		entry := entries[i]
		// A valid entry is the two status bytes, a space, then at least one
		// path byte. Anything shorter is the empty string after the final
		// NUL or a malformed record.
		if len(entry) < 4 {
			continue
		}
		status := entry[:2]
		path := entry[3:]
		if isRenameOrCopy(status) {
			// Consume the original path so it is not mistaken for an
			// entry of its own.
			i++
		}
		if word := worktreeStatusWord(status); word != "" {
			files = append(files, manifestWorktreeFile{Path: path, Status: word})
		}
		if len(files) >= limit {
			break
		}
	}
	return files
}

func isRenameOrCopy(status string) bool {
	return strings.ContainsAny(status, "RC")
}

// worktreeStatusWord reduces the two status columns to one word. The first
// column describes the index, the second the working tree; a file staged and
// then edited further reports both, and "modified" already covers that.
func worktreeStatusWord(status string) string {
	for _, c := range status {
		switch c {
		case '?':
			return "untracked"
		case 'M':
			return "modified"
		case 'A':
			return "added"
		case 'D':
			return "deleted"
		case 'R':
			return "renamed"
		case 'C':
			return "copied"
		case ' ', '!':
			continue
		}
	}
	return ""
}
