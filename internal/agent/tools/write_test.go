package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

type mockFileTrackerService struct{}

func (m mockFileTrackerService) RecordRead(ctx context.Context, sessionID, path string) {}

func (m mockFileTrackerService) LastReadTime(ctx context.Context, sessionID, path string) time.Time {
	return time.Now()
}

func (m mockFileTrackerService) ListReadFiles(ctx context.Context, sessionID string) ([]string, error) {
	return nil, nil
}

func TestWriteToolWritesEmptyNewFile(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	ctx := context.WithValue(t.Context(), SessionIDContextKey, "test-session")

	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workingDir, FileWriteOptions{})

	input, err := json.Marshal(WriteParams{FilePath: "empty.txt", Content: ""})
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  WriteToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	require.False(t, resp.IsError)

	b, err := os.ReadFile(filepath.Join(workingDir, "empty.txt"))
	require.NoError(t, err)
	require.Equal(t, "", string(b))
}

func runWriteTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params WriteParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "test-call",
		Name:  WriteToolName,
		Input: string(input),
	})
	require.NoError(t, err)
	return resp
}

func TestWriteToolAllowsUnreadExistingFileByDefault(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	path := filepath.Join(workingDir, "existing.txt")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))

	ctx := context.WithValue(t.Context(), SessionIDContextKey, "test-session")
	// mockFileTracker reports a zero last-read time, so there is no baseline.
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTracker{}, workingDir, FileWriteOptions{})

	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: "existing.txt", Content: "new"})
	require.False(t, resp.IsError)

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "new", string(b))
}

func TestWriteToolRequiresReadWhenStrict(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	path := filepath.Join(workingDir, "existing.txt")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))

	ctx := context.WithValue(t.Context(), SessionIDContextKey, "test-session")
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTracker{}, workingDir, FileWriteOptions{RequireRead: true})

	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: "existing.txt", Content: "new"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "you must read the file before writing it")

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old", string(b))
}

func TestWriteToolRejectsStaleRead(t *testing.T) {
	t.Parallel()

	workingDir := t.TempDir()
	path := filepath.Join(workingDir, "existing.txt")
	require.NoError(t, os.WriteFile(path, []byte("old"), 0o644))

	ctx := context.WithValue(t.Context(), SessionIDContextKey, "test-session")
	tracker := &mockEditFileTracker{lastRead: time.Now().Add(-2 * time.Hour)}
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, tracker, workingDir, FileWriteOptions{})

	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: "existing.txt", Content: "new"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "has been modified since it was last read")

	b, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Equal(t, "old", string(b))
}
