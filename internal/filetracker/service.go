// Package filetracker provides functionality to track file reads and writes
// in sessions.
package filetracker

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"time"

	"github.com/charmbracelet/crush/internal/db"
)

// EventKind is what a session did with a path. Reads have their own table
// because require-read-before-write depends on read_files; the other kinds
// live in file_events, which answers "what did this session change".
type EventKind string

// WriteEvent is one file the session created or modified, with the time of
// the latest write. The summary lists these as the session's own changes;
// git supplies the edits it cannot attribute.
type WriteEvent struct {
	Path string
	At   time.Time
}

const (
	// EventWrite is a file the session created or modified with a file tool.
	EventWrite EventKind = "write"
	// EventExec is a shell command the session ran. Its path is the command,
	// not a file, because the files a command touches cannot be known
	// reliably from its arguments.
	EventExec EventKind = "exec"
)

// Service defines the interface for tracking file activity in sessions.
type Service interface {
	// RecordRead records when a file was read.
	RecordRead(ctx context.Context, sessionID, path string)

	// LastReadTime returns when a file was last read.
	// Returns zero time if never read.
	LastReadTime(ctx context.Context, sessionID, path string) time.Time

	// ListReadFiles returns the paths of all files read in a session.
	ListReadFiles(ctx context.Context, sessionID string) ([]string, error)

	// RecordWrite records that the session created or modified a file. A
	// repeated write refreshes the timestamp rather than adding an entry.
	RecordWrite(ctx context.Context, sessionID, path string)

	// RecordExec records that the session ran a shell command. The command
	// is stored verbatim; it is not parsed for file paths.
	RecordExec(ctx context.Context, sessionID, command string)

	// ListRecentWrites returns the files the session wrote, newest first,
	// with the time of the latest write. Paths are absolute: the caller
	// shortens them against whatever workspace it is summarizing for, which
	// need not be the process working directory ListReadFiles resolves
	// against.
	ListRecentWrites(ctx context.Context, sessionID string, limit int) ([]WriteEvent, error)
}

type service struct {
	q *db.Queries
}

// NewService creates a new file tracker service.
func NewService(q *db.Queries) Service {
	return &service{q: q}
}

// RecordRead records when a file was read.
func (s *service) RecordRead(ctx context.Context, sessionID, path string) {
	if err := s.q.RecordFileRead(ctx, db.RecordFileReadParams{
		SessionID: sessionID,
		Path:      relpath(path),
	}); err != nil {
		slog.Error("Error recording file read", "error", err, "file", path)
	}
}

// RecordWrite records that the session created or modified a file.
func (s *service) RecordWrite(ctx context.Context, sessionID, path string) {
	s.recordEvent(ctx, sessionID, path, EventWrite)
}

// RecordExec records that the session ran a shell command.
func (s *service) RecordExec(ctx context.Context, sessionID, command string) {
	s.recordEvent(ctx, sessionID, command, EventExec)
}

// recordEvent writes one ledger entry. The ledger is advisory: the file or
// command already happened, so a failed insert is logged and never fails the
// tool whose action it describes.
//
// A write is stored as the absolute path the tool used. It is not made
// relative to the process working directory: read_files does that because
// require-read-before-write compares against a path derived the same way, but
// the summary needs a path it can shorten against the session's workspace,
// which need not be the process working directory.
func (s *service) recordEvent(ctx context.Context, sessionID, value string, kind EventKind) {
	if sessionID == "" || value == "" {
		return
	}
	entry := value
	if kind == EventWrite {
		if abs, err := filepath.Abs(value); err == nil {
			entry = abs
		}
	}
	if err := s.q.RecordFileEvent(ctx, db.RecordFileEventParams{
		SessionID: sessionID,
		Path:      entry,
		Kind:      string(kind),
		At:        time.Now().UnixMilli(),
	}); err != nil {
		slog.Error("Error recording file event", "error", err, "kind", kind, "value", value)
	}
}

// ListRecentWrites returns the files the session wrote, newest first.
func (s *service) ListRecentWrites(ctx context.Context, sessionID string, limit int) ([]WriteEvent, error) {
	if limit <= 0 {
		return nil, nil
	}
	events, err := s.q.ListRecentFileWrites(ctx, db.ListRecentFileWritesParams{
		SessionID: sessionID,
		Limit:     int64(limit),
	})
	if err != nil {
		return nil, fmt.Errorf("listing recent file writes: %w", err)
	}

	writes := make([]WriteEvent, 0, len(events))
	for _, event := range events {
		writes = append(writes, WriteEvent{
			Path: event.Path,
			At:   time.UnixMilli(event.At),
		})
	}
	return writes, nil
}

// LastReadTime returns when a file was last read.
// Returns zero time if never read.
func (s *service) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	readFile, err := s.q.GetFileRead(ctx, db.GetFileReadParams{
		SessionID: sessionID,
		Path:      relpath(path),
	})
	if err != nil {
		return time.Time{}
	}

	return time.Unix(readFile.ReadAt, 0)
}

func relpath(path string) string {
	path = filepath.Clean(path)
	basepath, err := os.Getwd()
	if err != nil {
		slog.Warn("Error getting basepath", "error", err)
		return path
	}
	relpath, err := filepath.Rel(basepath, path)
	if err != nil {
		slog.Warn("Error getting relpath", "error", err)
		return path
	}
	return relpath
}

// ListReadFiles returns the paths of all files read in a session.
func (s *service) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	readFiles, err := s.q.ListSessionReadFiles(ctx, sessionID)
	if err != nil {
		return nil, fmt.Errorf("listing read files: %w", err)
	}

	basepath, err := os.Getwd()
	if err != nil {
		return nil, fmt.Errorf("getting working directory: %w", err)
	}

	paths := make([]string, 0, len(readFiles))
	for _, rf := range readFiles {
		paths = append(paths, filepath.Join(basepath, rf.Path))
	}
	return paths, nil
}
