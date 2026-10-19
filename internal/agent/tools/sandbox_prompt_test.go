package tools

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/stretchr/testify/require"
)

// TestFileTools_ConfinedWriteDoesNotPrompt pins the central approval rule:
// only an operation that leaves the sandbox prompts. A write inside the
// writable roots is confined, so write, edit, and multiedit all run without
// an approval request.
func TestFileTools_ConfinedWriteDoesNotPrompt(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	ctx := sandboxTestContext(t, workspace)

	existing := filepath.Join(workspace, "existing.txt")
	require.NoError(t, os.WriteFile(existing, []byte("hello world"), 0o644))

	writeTool := NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	resp := runWriteTool(t, writeTool, ctx, WriteParams{
		FilePath: filepath.Join(workspace, "new.txt"),
		Content:  "x",
	})
	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount, "confined write must not prompt")

	editTool := NewEditTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	resp = runEditTool(t, editTool, ctx, EditParams{
		FilePath:  existing,
		OldString: "hello",
		NewString: "goodbye",
	})
	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount, "confined edit must not prompt")

	multiTool := NewMultiEditTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	resp = runMultiEditTool(t, multiTool, ctx, MultiEditParams{
		FilePath: existing,
		Edits:    []MultiEditOperation{{OldString: "goodbye", NewString: "hi"}},
	})
	require.False(t, resp.IsError)
	require.Equal(t, 0, perms.requestCount, "confined multiedit must not prompt")
}

// TestFileTools_OutsideWritePromptsOnce pins that the fence is the only
// approval a file write can raise, and that it is raised exactly once.
func TestFileTools_OutsideWritePromptsOnce(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	ctx := sandboxTestContext(t, workspace)

	tools := []struct {
		name string
		run  func() fantasy.ToolResponse
	}{
		{"write", func() fantasy.ToolResponse {
			return runWriteTool(t, NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{}), ctx,
				WriteParams{FilePath: filepath.Join(t.TempDir(), "f.txt"), Content: "x"})
		}},
		{"edit", func() fantasy.ToolResponse {
			return runEditTool(t, NewEditTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{}), ctx,
				EditParams{FilePath: filepath.Join(t.TempDir(), "f.txt"), NewString: "x"})
		}},
		{"multiedit", func() fantasy.ToolResponse {
			return runMultiEditTool(t, NewMultiEditTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{}), ctx,
				MultiEditParams{FilePath: filepath.Join(t.TempDir(), "f.txt"), Edits: []MultiEditOperation{{NewString: "x"}}})
		}},
	}
	for _, tc := range tools {
		perms.requestCount = 0
		perms.lastRequest = nil
		resp := tc.run()
		require.Falsef(t, resp.IsError, "%s: an approved fence lets the write proceed", tc.name)
		require.Equalf(t, 1, perms.requestCount, "%s: only the fence may prompt", tc.name)
		require.NotNil(t, perms.lastRequest)
		require.Truef(t, perms.lastRequest.Escalation, "%s: the prompt must be an escalation", tc.name)
	}
}

// TestHandleFenceDenials_AsksOnceForManyPaths pins that a single operation
// rewriting several out-of-policy files raises one dialog, not one per file.
func TestHandleFenceDenials_AsksOnceForManyPaths(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	ctx := sandboxTestContext(t, workspace)

	paths := []string{
		filepath.Join(t.TempDir(), "a.go"),
		filepath.Join(t.TempDir(), "b.go"),
	}
	call := fantasy.ToolCall{ID: "rename-call", Name: RenameToolName}

	_, denied, err := handleFenceDenials(ctx, perms, RenameToolName, paths, call, nil)
	require.NoError(t, err)
	require.Nil(t, denied)
	require.Equal(t, 1, perms.requestCount, "many refused paths must share one dialog")
	require.NotNil(t, perms.lastRequest)
	require.True(t, perms.lastRequest.Escalation)
	require.Equal(t, string(sandbox.ModeDangerFullAccess), perms.lastRequest.EscalationMode,
		"arbitrary out-of-tree paths need the widest mode")
}

// TestHandleFenceDenials_AsksForWorkspaceWriteWhenItSuffices verifies the
// escalation asks for the narrowest mode that covers the refused paths: a
// read-only session refusing a workspace write needs workspace-write, not
// the whole disk.
func TestHandleFenceDenials_AsksForWorkspaceWriteWhenItSuffices(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}

	policy := tableSandboxPolicy(t, workspace)
	readOnly := *policy
	readOnly.Mode = sandbox.ModeReadOnly
	readOnly.WriteRoots = nil
	ctx := WithSandboxPolicy(
		context.WithValue(context.Background(), SessionIDContextKey, "s"),
		&readOnly,
	)

	paths := []string{
		filepath.Join(workspace, "a.go"),
		filepath.Join(workspace, "b.go"),
	}
	_, denied, err := handleFenceDenials(ctx, perms, RenameToolName, paths, fantasy.ToolCall{ID: "rename"}, nil)
	require.NoError(t, err)
	require.Nil(t, denied)
	require.Equal(t, 1, perms.requestCount)
	require.Equal(t, string(sandbox.ModeWorkspaceWrite), perms.lastRequest.EscalationMode,
		"workspace-write is enough for workspace paths")
}

// TestCoveringMode_PicksTheNarrowestWiderMode verifies an escalation asks for
// only as much as the refused paths need.
func TestCoveringMode_PicksTheNarrowestWiderMode(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	readOnly := &sandbox.Policy{
		Mode:       sandbox.ModeReadOnly,
		Workspace:  workspace,
		WriteRoots: []string{t.TempDir()},
	}
	require.Equal(t, sandbox.ModeWorkspaceWrite,
		coveringMode(readOnly, []string{filepath.Join(workspace, "a.go")}))

	workspaceWrite := &sandbox.Policy{
		Mode:       sandbox.ModeWorkspaceWrite,
		Workspace:  workspace,
		WriteRoots: []string{workspace},
	}
	require.Equal(t, sandbox.ModeDangerFullAccess,
		coveringMode(workspaceWrite, []string{filepath.Join(t.TempDir(), "b.go")}))
}

// TestRequiresApproval_FollowsTheMode pins the single approval predicate used
// by bash, MCP, and the prompt helpers.
func TestRequiresApproval_FollowsTheMode(t *testing.T) {
	t.Parallel()

	confined := &sandbox.Policy{Mode: sandbox.ModeWorkspaceWrite}
	require.True(t, requiresApproval(confined, true))
	require.False(t, requiresApproval(confined, false))

	delegated := &sandbox.Policy{Mode: sandbox.ModeDangerFullAccess}
	require.False(t, requiresApproval(delegated, true),
		"danger-full-access is full delegation: nothing prompts")

	require.False(t, requiresApproval(nil, true),
		"with no policy attached there is no boundary to leave")
}

// TestEscalationLeavesContextUnchangedWhenNoPolicy verifies the fence is a
// no-op for callers that never attached a policy (hooks, diagnostics, tests).
func TestEscalationLeavesContextUnchangedWhenNoPolicy(t *testing.T) {
	t.Parallel()
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	ctx := context.WithValue(context.Background(), SessionIDContextKey, "s")

	_, denied, err := handleFenceDenials(ctx, perms, WriteToolName, []string{filepath.Join(t.TempDir(), "x")}, fantasy.ToolCall{ID: "c"}, nil)
	require.NoError(t, err)
	require.Nil(t, denied)
	require.Equal(t, 0, perms.requestCount)
}

// TestHandleFenceDenials_ApprovalWidensForEveryPath verifies an approved
// multi-file fence returns a policy under which every refused path is
// writable, so the caller can apply the whole operation.
func TestHandleFenceDenials_ApprovalWidensForEveryPath(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	ctx := sandboxTestContext(t, workspace)

	paths := []string{
		filepath.Join(t.TempDir(), "a.go"),
		filepath.Join(t.TempDir(), "b.go"),
	}
	ctx, denied, err := handleFenceDenials(ctx, perms, RenameToolName, paths, fantasy.ToolCall{ID: "rename"}, nil)
	require.NoError(t, err)
	require.Nil(t, denied)

	policy := SandboxPolicyFromContext(ctx)
	require.NotNil(t, policy)
	for _, path := range paths {
		require.NoError(t, policy.CheckWrite(path), "%s must be writable after approval", path)
	}
}

// TestDenyWrite_RefusedWithoutEscalation pins that a configured deny-write
// entry is not a boundary the user can be asked to cross: widening the mode
// would not make it writable, so no dialog appears.
func TestDenyWrite_RefusedWithoutEscalation(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	secrets := filepath.Join(workspace, "secrets")
	policy, err := sandbox.Resolve(sandbox.Options{
		Workspace: workspace,
		TempDir:   filepath.Join(t.TempDir(), "tmp"),
		Config:    sandbox.Config{Mode: sandbox.ModeWorkspaceWrite, DenyWrite: []string{secrets}},
	})
	require.NoError(t, err)

	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	ctx := WithSandboxPolicy(
		context.WithValue(context.Background(), SessionIDContextKey, "s"),
		policy,
	)

	resp := runWriteTool(t, NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{}), ctx,
		WriteParams{FilePath: filepath.Join(secrets, "token"), Content: "x"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "reason=deny-write")
	require.True(t, resp.StopTurn)
	require.Equal(t, 0, perms.requestCount, "a deny-write path must not raise an escalation dialog")
}
