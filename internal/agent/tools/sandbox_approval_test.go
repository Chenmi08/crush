package tools

import (
	"context"
	"path/filepath"
	"testing"

	"charm.land/fantasy"

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

// TestApproval_NetworkCommandPrompts verifies a command that leaves the
// sandbox's boundary prompts, and the request says it is confined.
func TestApproval_NetworkCommandPrompts(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", nil)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)

	resp := runBashTool(t, tool, ctx, BashParams{Description: "push", Command: "git push origin main"})
	require.False(t, resp.IsError)
	require.Equal(t, 1, perms.requestCount,
		"a network command must prompt")
	require.NotNil(t, perms.lastRequest)
	require.Contains(t, perms.lastRequest.Description, "network")
}

// TestApproval_NoPolicyNeverPrompts verifies that with no policy attached
// (hooks, diagnostics) nothing prompts: there is no boundary to leave.
func TestApproval_NoPolicyNeverPrompts(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", nil)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")

	resp := runBashTool(t, tool, ctx, BashParams{Description: "write", Command: "echo hi > inside.txt"})
	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount,
		"no policy means no approval question")
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

// TestApproval_RequestCarriesSandboxState verifies the request records the
// mode, so the prompt and audit subscribers can state the posture.
func TestApproval_RequestCarriesSandboxState(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	attribution := &config.Attribution{TrailerStyle: config.TrailerStyleNone}
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", nil)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)
	ctx = WithSandboxPolicy(ctx, policy)

	runBashTool(t, tool, ctx, BashParams{Description: "push", Command: "git push origin main"})
	require.NotNil(t, perms.lastRequest)
	require.True(t, perms.lastRequest.Sandboxed)
	require.Equal(t, "workspace-write", perms.lastRequest.SandboxMode)
}

// TestApproval_HookAndSessionGrantsDoNotBypassBoundary verifies the sandbox is
// independent of the approval mechanism: even when a permission service grants
// everything, an out-of-policy write only proceeds through the fence dialog —
// and a protected path is refused outright, no matter what grants exist.
func TestApproval_HookAndSessionGrantsDoNotBypassBoundary(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	// The granting mock approves the fence dialog for an ordinary outside
	// write, so it proceeds.
	outside := t.TempDir() + "/file.txt"
	resp := runWriteTool(t, tool, ctx, WriteParams{FilePath: outside, Content: "x"})
	require.False(t, resp.IsError)

	// A protected path is refused with no dialog at all: no grant can
	// re-allow it.
	target := filepath.Join(workspace, "crushrc")
	resp = runWriteTool(t, tool, ctx, WriteParams{FilePath: target, Content: "x"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, sandbox.Marker)
	require.Contains(t, resp.Content, "reason=protected")
}

// sandboxAttribution returns the attribution used by tool tests.
func sandboxAttribution() *config.Attribution {
	return &config.Attribution{TrailerStyle: config.TrailerStyleNone}
}

// TestApproval_LSPWriteToolsAreFenced pins that the LSP write tools use the
// same fence as write/edit/multiedit. Confinement must not depend on which
// tool the model picked; lsp_rename previously applied its workspace edit
// directly, with no policy check at all.
func TestApproval_LSPWriteToolsAreFenced(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)

	// The user declines the leave-sandbox prompt: the original denial comes
	// back to the model with the shared marker.
	deniedPerms := &recordingPermissionService{Broker: pubsubBroker(), allow: false}
	call := fantasy.ToolCall{ID: "call-lsp", Name: ReplaceSymbolToolName}

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)

	outside := filepath.Join(t.TempDir(), "outside.go")
	_, denied, err := handleFenceDenial(ctx, deniedPerms, RenameToolName, outside, call, nil)
	require.NoError(t, err)
	require.NotNil(t, denied, "the fence must refuse %s", outside)
	require.Contains(t, denied.Content, sandbox.Marker)
	require.True(t, denied.StopTurn)
	require.Equal(t, 1, deniedPerms.requestCount,
		"the refusal asks the user once via the leave-sandbox dialog")

	// A path inside the workspace is allowed through without a prompt: it
	// is confined, so there is no boundary to leave.
	inside := filepath.Join(workspace, "inside.go")
	_, denied, err = handleFenceDenial(ctx, deniedPerms, RenameToolName, inside, call, nil)
	require.NoError(t, err)
	require.Nil(t, denied)
	require.Equal(t, 1, deniedPerms.requestCount,
		"a confined write must not add another prompt")
}
