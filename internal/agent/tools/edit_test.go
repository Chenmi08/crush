package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/filetracker"
	"github.com/stretchr/testify/require"
)

type mockEditFileTracker struct {
	lastRead time.Time
	reads    []string
	writes   []string
}

func (m *mockEditFileTracker) RecordRead(ctx context.Context, sessionID, path string) {
	m.reads = append(m.reads, path)
}

func (m *mockEditFileTracker) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	return m.lastRead
}

func (m *mockEditFileTracker) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return m.reads, nil
}

func (m *mockEditFileTracker) RecordWrite(ctx context.Context, sessionID, path string) {
	m.writes = append(m.writes, path)
}

func (m *mockEditFileTracker) RecordExec(ctx context.Context, sessionID, command string) {}

func (m *mockEditFileTracker) ListRecentWrites(ctx context.Context, sessionID string, limit int) ([]filetracker.WriteEvent, error) {
	writes := make([]filetracker.WriteEvent, 0, len(m.writes))
	for _, path := range m.writes {
		writes = append(writes, filetracker.WriteEvent{Path: path})
	}
	return writes, nil
}

func TestReplaceContentPreservesCRLFAndMetadata(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\r\nbeta\r\n"), 0o644))

	tracker := &mockEditFileTracker{lastRead: time.Now().Add(time.Second)}
	edit := editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		files:       &mockHistoryService{},
		filetracker: tracker,
		workingDir:  dir,
	}

	resp, err := replaceContent(edit, filePath, "beta", "BETA", false)
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, "Content replaced in file: "+filePath, resp.Content)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\r\nBETA\r\n", string(content))
	require.Equal(t, []string{filePath}, tracker.reads)

	var meta EditResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.Equal(t, "alpha\nbeta\n", meta.OldContent)
	require.Equal(t, "alpha\r\nBETA\r\n", meta.NewContent)
}

func TestDeleteContentRejectsMultipleMatchesWithoutReplaceAll(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\nalpha\n"), 0o644))

	edit := editContext{
		ctx:         context.WithValue(t.Context(), SessionIDContextKey, "session"),
		files:       &mockHistoryService{},
		filetracker: &mockEditFileTracker{lastRead: time.Now().Add(time.Second)},
		workingDir:  dir,
	}

	resp, err := deleteContent(edit, filePath, "alpha\n", false)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "old_string appears multiple times")

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\nbeta\nalpha\n", string(content))
}

func newReadGuardEditContext(ctx context.Context, dir string, tracker *mockEditFileTracker, opts FileWriteOptions) editContext {
	return editContext{
		ctx:         context.WithValue(ctx, SessionIDContextKey, "session"),
		files:       &mockHistoryService{},
		filetracker: tracker,
		workingDir:  dir,
		opts:        opts,
	}
}

func TestReplaceContentAllowsUnreadFileByDefault(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644))

	// A zero last-read time means the session never read the file. The
	// default policy allows the edit rather than forcing a view first.
	edit := newReadGuardEditContext(t.Context(), dir, &mockEditFileTracker{}, FileWriteOptions{})

	resp, err := replaceContent(edit, filePath, "beta", "BETA", false)
	require.NoError(t, err)
	require.False(t, resp.IsError)
	require.Equal(t, "Content replaced in file: "+filePath, resp.Content)

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\nBETA\n", string(content))
}

func TestReplaceContentRequiresReadWhenStrict(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644))

	edit := newReadGuardEditContext(t.Context(), dir, &mockEditFileTracker{}, FileWriteOptions{RequireRead: true})

	resp, err := replaceContent(edit, filePath, "beta", "BETA", false)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "you must read the file before editing it")

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\nbeta\n", string(content))
}

func TestReplaceContentRejectsStaleRead(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	filePath := filepath.Join(dir, "test.txt")
	require.NoError(t, os.WriteFile(filePath, []byte("alpha\nbeta\n"), 0o644))

	// The file was read long before it changed on disk, so the baseline is
	// stale in both policies.
	edit := newReadGuardEditContext(t.Context(), dir, &mockEditFileTracker{lastRead: time.Now().Add(-2 * time.Hour)}, FileWriteOptions{})

	resp, err := replaceContent(edit, filePath, "beta", "BETA", false)
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "has been modified since it was last read")

	content, err := os.ReadFile(filePath)
	require.NoError(t, err)
	require.Equal(t, "alpha\nbeta\n", string(content))
}
