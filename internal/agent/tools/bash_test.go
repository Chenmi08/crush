package tools

import (
	"context"
	"encoding/json"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/charmbracelet/crush/internal/shell"
	"github.com/stretchr/testify/require"
)

type mockBashPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
}

func (m *mockBashPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	return true, nil
}

func (m *mockBashPermissionService) Grant(req permission.PermissionRequest) bool { return true }

func (m *mockBashPermissionService) Deny(req permission.PermissionRequest) bool { return true }

func (m *mockBashPermissionService) GrantPersistent(req permission.PermissionRequest) bool {
	return true
}

func (m *mockBashPermissionService) AutoApproveSession(sessionID string) {}

func (m *mockBashPermissionService) ActiveRequest() (permission.PermissionRequest, bool) {
	return permission.PermissionRequest{}, false
}

func (m *mockBashPermissionService) SetSkipRequests(skip bool) {}

func (m *mockBashPermissionService) SkipRequests() bool {
	return false
}

func (m *mockBashPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func TestBashTool_DefaultAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "default threshold",
		Command:     "echo done",
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.False(t, meta.Background)
	require.Empty(t, meta.ShellID)
	require.Contains(t, meta.Output, "done")
}

func TestBashTool_CustomAutoBackgroundThreshold(t *testing.T) {
	workingDir := t.TempDir()
	tool := newBashToolForTest(workingDir)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{
		Description:         "custom threshold",
		Command:             "sleep 1.5 && echo done",
		AutoBackgroundAfter: 1,
	})

	require.False(t, resp.IsError)
	var meta BashResponseMetadata
	require.NoError(t, json.Unmarshal([]byte(resp.Metadata), &meta))
	require.True(t, meta.Background)
	require.NotEmpty(t, meta.ShellID)
	require.Contains(t, resp.Content, "moved to background")

	bgManager := shell.GetBackgroundShellManager()
	require.NoError(t, bgManager.Kill(meta.ShellID))
}

type recordingPermissionService struct {
	*pubsub.Broker[permission.PermissionRequest]
	requestCount int
	allow        bool
	lastRequest  *permission.CreatePermissionRequest
}

func (m *recordingPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	m.requestCount++
	m.lastRequest = &req
	return m.allow, nil
}

func (m *recordingPermissionService) Grant(req permission.PermissionRequest) bool { return true }

func (m *recordingPermissionService) Deny(req permission.PermissionRequest) bool { return true }

func (m *recordingPermissionService) GrantPersistent(req permission.PermissionRequest) bool {
	return true
}

func (m *recordingPermissionService) AutoApproveSession(sessionID string) {}

func (m *recordingPermissionService) ActiveRequest() (permission.PermissionRequest, bool) {
	return permission.PermissionRequest{}, false
}

func (m *recordingPermissionService) SetSkipRequests(skip bool) {}

func (m *recordingPermissionService) SkipRequests() bool {
	return false
}

func (m *recordingPermissionService) SubscribeNotifications(ctx context.Context) <-chan pubsub.Event[permission.PermissionNotification] {
	return make(<-chan pubsub.Event[permission.PermissionNotification])
}

func newBashToolForTest(workingDir string) fantasy.AgentTool {
	permissions := &mockBashPermissionService{Broker: pubsub.NewBroker[permission.PermissionRequest]()}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	return NewBashTool(permissions, workingDir, workingDir, attribution, "test-model", nil)
}

func newBashToolWithRecordingPerms(workingDir string, allow bool) (fantasy.AgentTool, *recordingPermissionService) {
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  allow,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	return NewBashTool(perms, workingDir, workingDir, attribution, "test-model", nil), perms
}

func TestBashTool_ConfinedChainedCommandsRunWithoutPrompt(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, true)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, tableSandboxPolicy(t, workingDir))

	// Chaining no longer decides approval: the sandbox does. A confined
	// chain runs without a permission request.
	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "chained ls",
		Command:     "ls && echo done",
	})

	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount,
		"a confined chain should not prompt")

	// A remote-mutating command in the chain still prompts.
	resp = runBashTool(t, tool, ctx, BashParams{
		Description: "chained push",
		Command:     "ls && git push origin main",
	})
	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount, "a chain containing a remote mutation should prompt")
}

func TestBashTool_NetworkCommandDenied(t *testing.T) {
	workingDir := t.TempDir()
	tool, perms := newBashToolWithRecordingPerms(workingDir, false)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, tableSandboxPolicy(t, workingDir))

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "push denied",
		Command:     "git push origin main",
	})

	require.Equal(t, 1, perms.requestCount)
	require.Contains(t, resp.Content, "User denied permission")
}

// TestBashTool_NetworkCommandRequiresPermission verifies the single remaining
// approval rule: a command that leaves the sandbox's boundary (network,
// privilege escalation) prompts even though writes are fenced.
func TestBashTool_NetworkCommandRequiresPermission(t *testing.T) {
	workingDir := t.TempDir()
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  true,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", nil)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, tableSandboxPolicy(t, workingDir))

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "push",
		Command:     "git push origin main",
	})
	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount,
		"a network command must request permission")
	require.NotNil(t, perms.lastRequest)
	require.Contains(t, perms.lastRequest.Description, "network")

	// A confined command stays auto-approved.
	perms.requestCount = 0
	resp = runBashTool(t, tool, ctx, BashParams{
		Description: "list branches",
		Command:     "git branch --list",
	})
	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount,
		"a confined command must not request permission")
}

// TestBashTool_FullAccessNeverPrompts verifies danger-full-access is full
// delegation: nothing prompts, not even network commands.
func TestBashTool_FullAccessNeverPrompts(t *testing.T) {
	workingDir := t.TempDir()
	perms := &recordingPermissionService{
		Broker: pubsub.NewBroker[permission.PermissionRequest](),
		allow:  true,
	}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workingDir, workingDir, attribution, "test-model", nil)
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	policy, err := sandbox.Resolve(sandbox.Options{Workspace: workingDir, TempDir: filepath.Join(t.TempDir(), "tmp")})
	require.NoError(t, err)
	policy.Mode = sandbox.ModeDangerFullAccess
	ctx = WithSandboxPolicy(ctx, policy)

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "push",
		Command:     "git push origin main",
	})
	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount,
		"danger-full-access must not prompt")
}

func runBashTool(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params BashParams) fantasy.ToolResponse {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)

	call := fantasy.ToolCall{
		ID:    "test-call",
		Name:  BashToolName,
		Input: string(input),
	}

	resp, err := tool.Run(ctx, call)
	require.NoError(t, err)
	return resp
}

func TestTruncateOutputValidUTF8(t *testing.T) {
	t.Parallel()
	// CJK characters are 2 cells wide; this string is far wider than
	// MaxOutputLength so TruncateOutput must truncate it.
	content := strings.Repeat("你好世界", MaxOutputLength)

	out := TruncateOutput(content, t.TempDir())
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

func TestTruncateOutputShortContent(t *testing.T) {
	t.Parallel()
	content := "short output"
	require.Equal(t, content, TruncateOutput(content, t.TempDir()))
}

func TestTruncateOutputEmoji(t *testing.T) {
	t.Parallel()
	// Emoji with ZWJ sequences should not be split.
	content := strings.Repeat("👨‍👩‍👧‍👦", MaxOutputLength)

	out := TruncateOutput(content, t.TempDir())
	require.True(t, utf8.ValidString(out), "truncated output must stay valid UTF-8")
	require.Contains(t, out, "lines truncated")
}

func TestIsNetworkCommand(t *testing.T) {
	t.Parallel()

	require.True(t, isNetworkCommand("ssh host", nil))
	require.True(t, isNetworkCommand("ssh", nil), "a bare command name matches")
	require.True(t, isNetworkCommand("git push origin main", nil),
		"multi-token entries match the leading tokens in order")
	require.True(t, isNetworkCommand("sudo apt install jq", nil))
	require.True(t, isNetworkCommand("docker push repo/img", []string{"docker push"}),
		"configured additions join the built-in list")
	require.False(t, isNetworkCommand("curl https://example.com", nil),
		"read-only egress is not a remote mutation")
	require.False(t, isNetworkCommand("git pushdry", nil), "tokens must match whole")
	require.False(t, isNetworkCommand("ls -la", nil))
	require.False(t, isNetworkCommand("", nil))
}
