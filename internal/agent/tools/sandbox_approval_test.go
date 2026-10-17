package tools

import (
	"context"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/stretchr/testify/require"
)

// pubsubBroker returns a fresh broker for the recording permission service.
func pubsubBroker() *pubsub.Broker[permission.PermissionRequest] {
	return pubsub.NewBroker[permission.PermissionRequest]()
}

// TestApproval_ConfinedCommandIsAutoApproved verifies the sandbox pays for
// itself: a confined command runs without a permission request.
func TestApproval_ConfinedCommandIsAutoApproved(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", nil)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)

	resp := runBashTool(t, tool, ctx, BashParams{Description: "write inside", Command: "echo hi > inside.txt"})
	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount,
		"a confined command must not prompt")
}

// TestApproval_DangerousCommandStillPrompts verifies a configured dangerous
// command prompts even when it is confined.
func TestApproval_DangerousCommandStillPrompts(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", []string{"git push"})

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)

	resp := runBashTool(t, tool, ctx, BashParams{Description: "push", Command: "git push origin main"})
	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount,
		"a dangerous command must prompt even when confined")
	require.NotNil(t, perms.lastRequest)
	require.True(t, perms.lastRequest.Dangerous)
}

// TestApproval_ExcludedCommandPromptsUnsandboxed verifies an excluded command
// prompts and the prompt says it runs unsandboxed.
func TestApproval_ExcludedCommandPromptsUnsandboxed(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", nil)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)
	ctx = WithSandboxExclusions(ctx, []string{"docker *"})

	resp := runBashTool(t, tool, ctx, BashParams{Description: "docker", Command: "docker ps"})
	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount)
	require.NotNil(t, perms.lastRequest)
	require.False(t, perms.lastRequest.Sandboxed)
	require.Contains(t, perms.lastRequest.Description, "unsandboxed")
	require.Contains(t, perms.lastRequest.Description, "excluded from the sandbox")
}

// TestApproval_NoBackendPromptsUnsandboxed verifies a confined mode with no
// usable backend prompts rather than running silently, and the prompt states
// it is unsandboxed.
func TestApproval_NoBackendPromptsUnsandboxed(t *testing.T) {
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)

	sandbox.ResetSelectionForTest()
	t.Cleanup(sandbox.ResetSelectionForTest)
	sandbox.ForceSelectionForTest(sandbox.Selection{Enforcement: sandbox.EnforcementNone, Reason: "no backend"})

	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", nil)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)

	resp := runBashTool(t, tool, ctx, BashParams{Description: "no backend", Command: "echo hi > inside.txt"})
	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount,
		"a command with no usable backend must prompt")
	require.NotNil(t, perms.lastRequest)
	require.False(t, perms.lastRequest.Sandboxed)
	require.Contains(t, perms.lastRequest.Description, "unsandboxed")
	require.Contains(t, perms.lastRequest.Description, "no sandbox backend")
}

// TestApproval_YoloKeepsBoundary verifies YOLO disables prompts but not the
// boundary: the sandbox policy is still attached and still refuses writes
// outside the roots.
func TestApproval_YoloKeepsBoundary(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)

	// YOLO is modeled by a permission service that always allows; the
	// boundary must still be enforced by the fence.
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", nil)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)

	outside := t.TempDir() + "/escape.txt"
	resp := runBashTool(t, tool, ctx, BashParams{Description: "escape", Command: "echo hi > " + outside})
	require.Equal(t, 0, perms.requestCount, "YOLO means no prompts")
	require.Contains(t, resp.Content, sandbox.Marker,
		"YOLO must not disable the boundary")
}

// TestApproval_RequestCarriesSandboxState verifies the request records whether
// the operation was sandboxed, so the prompt and audit subscribers can
// distinguish the two.
func TestApproval_RequestCarriesSandboxState(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", nil)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)
	ctx = WithSandboxExclusions(ctx, []string{"docker *"})

	runBashTool(t, tool, ctx, BashParams{Description: "docker", Command: "docker ps"})
	require.NotNil(t, perms.lastRequest)
	require.False(t, perms.lastRequest.Sandboxed)
	require.Equal(t, "workspace-write", perms.lastRequest.SandboxMode,
		"the request states the mode that would have applied")
}

// TestApproval_HookAndSessionGrantsDoNotBypassBoundary verifies the sandbox is
// independent of the approval mechanism: even when a permission service
// grants everything, the fence still refuses an out-of-policy write.
func TestApproval_HookAndSessionGrantsDoNotBypassBoundary(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)
	_ = policy

	outside := t.TempDir() + "/file.txt"
	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: outside, Content: "x"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, sandbox.Marker)
}

// sandboxAttribution returns the attribution used by tool tests.
func sandboxAttribution() *config.Attribution {
	return &config.Attribution{TrailerStyle: config.TrailerStyleNone}
}
