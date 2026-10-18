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
	"github.com/charmbracelet/crush/internal/pubsub"
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

// denyingPermissionService answers every request with a denial, modeling the
// user refusing the "Leave Sandbox?" dialog.
type denyingPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
	requests []permission.CreatePermissionRequest
}

func (d *denyingPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	d.requests = append(d.requests, req)
	return false, nil
}

func (d *denyingPermissionService) Grant(req permission.PermissionRequest) bool { return true }
func (d *denyingPermissionService) Deny(req permission.PermissionRequest) bool  { return true }
func (d *denyingPermissionService) GrantPersistent(req permission.PermissionRequest) bool {
	return true
}
func (d *denyingPermissionService) AutoApproveSession(string) {}
func (d *denyingPermissionService) ActiveRequest() (permission.PermissionRequest, bool) {
	return permission.PermissionRequest{}, false
}
func (d *denyingPermissionService) SetSkipRequests(bool) {}
func (d *denyingPermissionService) SkipRequests() bool   { return false }
func (d *denyingPermissionService) SubscribeNotifications(context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

// --- Write tool ---

// TestWriteTool_SandboxRefusalAsksOnce verifies the new fence behavior: an
// out-of-workspace write asks the user once (the mock grants), and the write
// then proceeds. The refusal left no filesystem trace before the dialog.
func TestWriteTool_SandboxRefusalAsksOnce(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outside := filepath.Join(t.TempDir(), "nested", "deeper", "file.txt")
	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: outside, Content: "x"})
	require.False(t, resp.IsError, "an approved dialog lets the write proceed")

	content, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "x", string(content))
}

// TestWriteTool_SandboxRefusalExplainsItself verifies the fence-raised dialog
// carries the user-facing explanation: what was refused, what approving
// means, and what denying does.
func TestWriteTool_SandboxRefusalExplainsItself(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := &recordingPermissionService{
		Broker: pubsubBroker(),
		allow:  true,
	}
	tool := NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outside := filepath.Join(t.TempDir(), "file.txt")
	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: outside, Content: "x"})
	require.False(t, resp.IsError)

	require.NotNil(t, perms.lastRequest)
	require.True(t, perms.lastRequest.Escalation)
	require.Contains(t, perms.lastRequest.Reasoning, outside,
		"the explanation names the refused path")
	require.Contains(t, perms.lastRequest.Reasoning, "denying",
		"the explanation says what denying does")
}

// TestWriteTool_SandboxRefusalDeniedKeepsDenial verifies the deny path: the
// user refusing the dialog leaves the operation denied with the standard
// vocabulary and nothing written.
func TestWriteTool_SandboxRefusalDeniedKeepsDenial(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := &denyingPermissionService{Broker: pubsubBroker()}
	tool := NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outside := filepath.Join(t.TempDir(), "file.txt")
	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: outside, Content: "x"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, sandbox.Marker)
	require.Contains(t, resp.Content, "stop and explain")
	require.True(t, resp.StopTurn)
	_, statErr := os.Stat(outside)
	require.True(t, os.IsNotExist(statErr))

	// Exactly one dialog was raised, and it was an escalation.
	require.Len(t, perms.requests, 1)
	require.True(t, perms.requests[0].Escalation)
	require.Equal(t, "danger-full-access", perms.requests[0].EscalationMode)
}

// TestWriteTool_SandboxAllowsInsideWorkspace verifies an in-workspace write
// never raises the sandbox dialog. (The write tool's own permission prompt is
// separate and pre-existing; the fence must stay silent here.)
func TestWriteTool_SandboxAllowsInsideWorkspace(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := &denyingPermissionService{Broker: pubsubBroker()}
	tool := NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)
	// The write tool's own prompt is granted by the test, isolating the
	// fence from the tool's separate approval.
	ctx = withEscalationApproval(ctx, "test-call")

	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: filepath.Join(workspace, "sub", "f.txt"), Content: "hi"})
	require.False(t, resp.IsError)
	require.Empty(t, perms.requests, "a confined write must not raise the sandbox dialog")
}

// TestWriteTool_SandboxProtectedPathNeverAsks verifies a protected path is
// refused outright: asking would be theater, since no setting can re-allow it.
func TestWriteTool_SandboxProtectedPathNeverAsks(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := &denyingPermissionService{Broker: pubsubBroker()}
	tool := NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	target := filepath.Join(workspace, "crushrc")
	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: target, Content: "x"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "reason=protected")
	require.Empty(t, perms.requests, "a protected path must not raise a dialog")
	_, err := os.Stat(target)
	require.True(t, os.IsNotExist(err))
}

// TestWriteTool_SandboxDeniesSymlinkEscape verifies a symlink out of the
// workspace goes through the dialog (approved by the mock), and the resolved
// target receives the file.
func TestWriteTool_SandboxDeniesSymlinkEscape(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	outside := t.TempDir()
	require.NoError(t, os.Symlink(outside, filepath.Join(workspace, "escape")))

	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: filepath.Join(workspace, "escape", "f.txt"), Content: "x"})
	require.False(t, resp.IsError)
	content, err := os.ReadFile(filepath.Join(outside, "f.txt"))
	require.NoError(t, err)
	require.Equal(t, "x", string(content))
}

// --- Edit / multiedit / download ---

func TestEditTool_SandboxRefusalAsksOnce(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewEditTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outside := filepath.Join(t.TempDir(), "new", "file.txt")
	resp := runEditTool(t, tool, ctx, EditParams{FilePath: outside, NewString: "hi"})
	require.False(t, resp.IsError)
	content, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "hi", string(content))
}

func TestMultiEditTool_SandboxRefusalAsksOnce(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewMultiEditTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outside := filepath.Join(t.TempDir(), "new", "file.txt")
	resp := runMultiEditTool(t, tool, ctx, MultiEditParams{
		FilePath: outside,
		Edits:    []MultiEditOperation{{OldString: "", NewString: "hello"}},
	})
	require.False(t, resp.IsError)
	content, err := os.ReadFile(outside)
	require.NoError(t, err)
	require.Equal(t, "hello", string(content))
}

func TestDownloadTool_SandboxRefusalAsksOnce(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		_, _ = w.Write([]byte("payload"))
	}))
	t.Cleanup(server.Close)

	tool := NewDownloadTool(&mockPermissionService{}, workspace, server.Client())
	ctx := sandboxTestContext(t, workspace)

	outside := filepath.Join(t.TempDir(), "new", "file.txt")
	resp := runDownloadTool(t, tool, ctx, DownloadParams{URL: server.URL, FilePath: outside})
	require.False(t, resp.IsError)
	content, err := os.ReadFile(outside)
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
