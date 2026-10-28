package agent

import (
	"context"
	"log/slog"
	"path/filepath"
	"strings"
)

// summaryWrites converts the session's write ledger into manifest entries,
// with paths shortened against workingDir.
//
// The ledger is the authoritative answer to "what did this session write": it
// is recorded by the file tools themselves, so a file renamed or replaced by
// an LSP tool is listed without teaching the manifest another tool name. git
// still covers edits made through a shell command, which no tool input can
// name reliably.
//
// A missing tracker or a failed read leaves the list empty rather than
// failing the compaction: git still covers the working tree, and refusing to
// summarize would wedge a session that is already over its window.
func (a *sessionAgent) summaryWrites(ctx context.Context, sessionID, workingDir string) []manifestFile {
	if a.filetracker == nil {
		return nil
	}
	events, err := a.filetracker.ListRecentWrites(ctx, sessionID, manifestWriteLedgerLimit)
	if err != nil {
		slog.Warn(
			"Could not read file writes for summary manifest",
			"session_id", sessionID,
			"error", err,
		)
		return nil
	}
	writes := make([]manifestFile, 0, len(events))
	for _, event := range events {
		if event.Path == "" {
			continue
		}
		writes = append(writes, manifestFile{
			Path: displayPath(event.Path, workingDir),
			Last: event.At.Unix(),
		})
	}
	return writes
}

// displayPath shortens a path to a workspace-relative one when it is inside
// workingDir. A relative path that escapes (starts with `..`) is not shorter,
// so the absolute form is kept instead.
func displayPath(path, workingDir string) string {
	if workingDir == "" {
		return path
	}
	rel, err := filepath.Rel(workingDir, path)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		return path
	}
	return filepath.ToSlash(rel)
}
