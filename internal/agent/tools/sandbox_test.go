package tools

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/stretchr/testify/require"
)

// tableSandboxPolicy builds a policy whose workspace is a temp directory and
// whose writable roots are exactly that workspace plus a private temp dir.
func tableSandboxPolicy(t *testing.T, workspace string) *sandbox.Policy {
	t.Helper()
	root := t.TempDir()
	policy, err := sandbox.Resolve(sandbox.Options{
		Workspace: workspace,
		TempDir:   filepath.Join(root, "tmp"),
		CacheDir:  filepath.Join(root, "cache"),
	})
	require.NoError(t, err)
	return policy
}

// sandboxTestContext attaches a policy and a session to ctx.
func sandboxTestContext(t *testing.T, workspace string) context.Context {
	t.Helper()
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	return WithSandboxPolicy(ctx, tableSandboxPolicy(t, workspace))
}

// genericRun marshals params and runs the tool, mirroring the per-tool
// helpers the other tests use.
func genericRun[T any](t *testing.T, tool fantasy.AgentTool, ctx context.Context, params T) fantasy.ToolResponse {
	t.Helper()
	input, err := json.Marshal(params)
	require.NoError(t, err)
	resp, err := tool.Run(ctx, fantasy.ToolCall{ID: "test-call", Name: tool.Info().Name, Input: string(input)})
	require.NoError(t, err)
	return resp
}

func runEditTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params EditParams) fantasy.ToolResponse {
	t.Helper()
	return genericRun(t, tool, ctx, params)
}

func runMultiEditTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params MultiEditParams) fantasy.ToolResponse {
	t.Helper()
	return genericRun(t, tool, ctx, params)
}

func runDownloadTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params DownloadParams) fantasy.ToolResponse {
	t.Helper()
	return genericRun(t, tool, ctx, params)
}

// CreatePermissionRequestStub returns an empty request for the sandbox-state
// assertions.
func CreatePermissionRequestStub() permission.CreatePermissionRequest {
	return permission.CreatePermissionRequest{}
}

func TestWriteTool_SandboxDeniesOutsideWorkspace(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outsideDir := filepath.Join(t.TempDir(), "nested", "deeper")
	outside := filepath.Join(outsideDir, "file.txt")

	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: outside, Content: "x"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, sandbox.Marker)
	require.Contains(t, resp.Content, outside)
	require.Contains(t, resp.Content, "mode=workspace-write")
	require.Contains(t, resp.Content, "remedy=")
	require.True(t, resp.StopTurn, "a policy denial must stop the turn")

	_, err := os.Stat(outsideDir)
	require.True(t, os.IsNotExist(err), "a denied write must leave no directory behind")
}

func TestWriteTool_SandboxAllowsInsideWorkspace(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	target := filepath.Join(workspace, "sub", "file.txt")
	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: target, Content: "hello"})
	require.False(t, resp.IsError)

	content, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "hello", string(content))
}

func TestWriteTool_SandboxDeniesProtectedPath(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	for _, name := range []string{"crushrc", "crush.json", "AGENTS.md", "CLAUDE.md"} {
		target := filepath.Join(workspace, name)
		resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: target, Content: "x"})
		require.True(t, resp.IsError, name)
		require.Contains(t, resp.Content, sandbox.Marker, name)
		require.Contains(t, resp.Content, "reason=protected", name)
		_, err := os.Stat(target)
		require.True(t, os.IsNotExist(err), "%s must not be created", name)
	}

	gitConfig := filepath.Join(workspace, ".git", "config")
	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: gitConfig, Content: "x"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "reason=protected")
	_, err := os.Stat(filepath.Join(workspace, ".git"))
	require.True(t, os.IsNotExist(err), "a denied write must not create .git")
}

func TestWriteTool_SandboxDeniesSymlinkEscape(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, "escape")))

	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	target := filepath.Join(workspace, "escape", "file.txt")
	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: target, Content: "x"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, sandbox.Marker)

	_, err := os.Stat(filepath.Join(outside, "file.txt"))
	require.True(t, os.IsNotExist(err), "the symlink target must not be written")
}

func TestEditTool_SandboxDeniesOutsideWorkspace(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewEditTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outsideDir := filepath.Join(t.TempDir(), "new")
	target := filepath.Join(outsideDir, "file.txt")

	// Creation branch.
	resp := runEditTool(t, tool, ctx, EditParams{FilePath: target, NewString: "hello"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, sandbox.Marker)
	_, err := os.Stat(outsideDir)
	require.True(t, os.IsNotExist(err), "a denied edit must not create parent directories")
}

func TestMultiEditTool_SandboxDeniesOutsideWorkspace(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewMultiEditTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outsideDir := filepath.Join(t.TempDir(), "new")
	target := filepath.Join(outsideDir, "file.txt")

	resp := runMultiEditTool(t, tool, ctx, MultiEditParams{
		FilePath: target,
		Edits:    []MultiEditOperation{{OldString: "", NewString: "hello"}},
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, sandbox.Marker)
	_, err := os.Stat(outsideDir)
	require.True(t, os.IsNotExist(err), "a denied multiedit must not create parent directories")
}

func TestDownloadTool_SandboxDeniesBeforeCreatingDirectories(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewDownloadTool(&mockPermissionService{}, workspace, nil)
	ctx := sandboxTestContext(t, workspace)

	outsideDir := filepath.Join(t.TempDir(), "new")
	target := filepath.Join(outsideDir, "file.txt")

	resp := runDownloadTool(t, tool, ctx, DownloadParams{
		URL:      "http://example.invalid/file",
		FilePath: target,
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, sandbox.Marker)
	_, err := os.Stat(outsideDir)
	require.True(t, os.IsNotExist(err), "a denied download must not create directories")
}

func TestDownloadTool_SandboxAllowsInsideWorkspace(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(server.Close)

	tool := NewDownloadTool(&mockPermissionService{}, workspace, server.Client())
	ctx := sandboxTestContext(t, workspace)

	target := filepath.Join(workspace, "sub", "file.txt")
	resp := runDownloadTool(t, tool, ctx, DownloadParams{URL: server.URL, FilePath: target})
	require.False(t, resp.IsError)

	content, err := os.ReadFile(target)
	require.NoError(t, err)
	require.Equal(t, "payload", string(content))
}

// TestSandboxedRequest_CarriesSandboxState verifies the permission request
// distinguishes a confined write from an unconfined one, which the prompt and
// audit subscribers depend on.
func TestSandboxedRequest_CarriesSandboxState(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	ctx := sandboxTestContext(t, workspace)

	req := sandboxedRequest(ctx, CreatePermissionRequestStub())
	require.True(t, req.Sandboxed)
	require.Equal(t, "workspace-write", req.SandboxMode)

	require.False(t, sandboxedRequest(context.Background(), CreatePermissionRequestStub()).Sandboxed)
}
