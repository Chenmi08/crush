package tools

import (
	"context"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/stretchr/testify/require"
)

// escalationPermissionService records requests and answers them from a queue.
type escalationPermissionService struct {
	recordingPermissionService
	// grants is consumed one decision per request; the last value repeats.
	grants []bool
}

func (s *escalationPermissionService) Request(ctx context.Context, req permission.CreatePermissionRequest) (bool, error) {
	s.requestCount++
	s.lastRequest = &req
	if len(s.grants) == 0 {
		return false, nil
	}
	grant := s.grants[0]
	if len(s.grants) > 1 {
		s.grants = s.grants[1:]
	}
	return grant, nil
}

func newEscalationService(grants ...bool) *escalationPermissionService {
	return &escalationPermissionService{
		recordingPermissionService: recordingPermissionService{
			Broker: pubsubBroker(),
			allow:  true,
		},
		grants: grants,
	}
}

// TestEscalation_PairValidation verifies one half without the other is
// malformed and rejected before execution.
func TestEscalation_PairValidation(t *testing.T) {
	t.Parallel()

	_, err := parseEscalation(EscalationParams{SandboxPermissions: "danger-full-access"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "justification")

	_, err = parseEscalation(EscalationParams{Justification: "I need to write outside."})
	require.Error(t, err)
	require.Contains(t, err.Error(), "sandbox_permissions")

	_, err = parseEscalation(EscalationParams{SandboxPermissions: "bogus", Justification: "because"})
	require.Error(t, err)
	require.Contains(t, err.Error(), "accepted")

	req, err := parseEscalation(EscalationParams{
		SandboxPermissions: "danger-full-access",
		Justification:      "Writing the release artefact outside the repository.",
	})
	require.NoError(t, err)
	require.Equal(t, sandbox.ModeDangerFullAccess, req.mode)

	none, err := parseEscalation(EscalationParams{})
	require.NoError(t, err)
	require.Nil(t, none)
}

// TestEscalation_RejectedBeforeExecution verifies a malformed pair never
// reaches the filesystem: no file and no directory are created.
func TestEscalation_RejectedBeforeExecution(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	target := filepath.Join(t.TempDir(), "nested", "file.txt")
	resp := runWriteTool(t, tool, ctx, WriteParams{
		FilePath: target,
		Content:  "x",
		EscalationParams: EscalationParams{
			// Missing justification.
			SandboxPermissions: "danger-full-access",
		},
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "justification")
	_, err := statPath(target)
	require.True(t, isNotExist(err))
}

// TestEscalation_MustBeStrictlyWider verifies a request for the same or a
// narrower mode performs no approval and executes under the effective policy.
func TestEscalation_MustBeStrictlyWider(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := newEscalationService(true)
	tool := NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outside := filepath.Join(t.TempDir(), "file.txt")
	resp := runWriteTool(t, tool, ctx, WriteParams{
		FilePath: outside,
		Content:  "x",
		EscalationParams: EscalationParams{
			SandboxPermissions: "workspace-write",
			Justification:      "Same mode, not wider.",
		},
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "escalation-not-wider")
	require.Equal(t, 0, perms.requestCount, "no approval is requested for a non-wider mode")
}

// TestEscalation_AppliesToExactlyOneCall verifies approval widens the policy
// for the approved call only, and is never recorded as a session grant.
func TestEscalation_AppliesToExactlyOneCall(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := newEscalationService(true, false)
	tool := NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outside := filepath.Join(t.TempDir(), "first.txt")
	resp := runWriteTool(t, tool, ctx, WriteParams{
		FilePath: outside,
		Content:  "x",
		EscalationParams: EscalationParams{
			SandboxPermissions: "danger-full-access",
			Justification:      "Writing outside the workspace once.",
		},
	})
	require.False(t, resp.IsError, "the approved escalation must let the call proceed")
	content, err := readFile(outside)
	require.NoError(t, err)
	require.Equal(t, "x", string(content))

	require.NotNil(t, perms.lastRequest)
	require.True(t, perms.lastRequest.Escalation)
	require.Equal(t, "danger-full-access", perms.lastRequest.EscalationMode)

	// The widened policy must not leak into the next call: the same context,
	// no escalation params, is confined again.
	second := filepath.Join(t.TempDir(), "second.txt")
	resp = runWriteTool(t, tool, ctx, WriteParams{FilePath: second, Content: "y"})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, sandbox.Marker)
	_, statErr := statPath(second)
	require.True(t, isNotExist(statErr))
}

// TestEscalation_RejectionStopsTheTurn verifies a denied escalation leaves the
// operation denied and instructs the model to stop rather than work around it.
func TestEscalation_RejectionStopsTheTurn(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := newEscalationService(false)
	tool := NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	outside := filepath.Join(t.TempDir(), "file.txt")
	resp := runWriteTool(t, tool, ctx, WriteParams{
		FilePath: outside,
		Content:  "x",
		EscalationParams: EscalationParams{
			SandboxPermissions: "danger-full-access",
			Justification:      "Need to write outside.",
		},
	})
	require.True(t, resp.IsError)
	require.True(t, resp.StopTurn, "the turn must stop so the model does not retry another route")
	require.Contains(t, resp.Content, "escalation-denied")
	require.Contains(t, resp.Content, "do not retry")
	_, err := statPath(outside)
	require.True(t, isNotExist(err))
}

// TestEscalation_RequestIsDistinct verifies the request is marked as an
// escalation with its target mode, which the UI renders distinctly.
func TestEscalation_RequestIsDistinct(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	perms := newEscalationService(false)
	tool := NewWriteTool(nil, perms, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	runWriteTool(t, tool, ctx, WriteParams{
		FilePath: filepath.Join(t.TempDir(), "x"),
		Content:  "x",
		EscalationParams: EscalationParams{
			SandboxPermissions: "danger-full-access",
			Justification:      "Need to write outside.",
		},
	})
	require.NotNil(t, perms.lastRequest)
	require.True(t, perms.lastRequest.Escalation)
	require.Equal(t, "danger-full-access", perms.lastRequest.EscalationMode)
	require.False(t, perms.lastRequest.Sandboxed,
		"the requested operation is not sandboxed, which the prompt must state")
	require.Contains(t, perms.lastRequest.Description, "Leave the sandbox")
}

// TestEscalation_DenialCarriesHint verifies the model-facing denial carries
// the mode marker followed by an escalation hint, in shared wording.
func TestEscalation_DenialCarriesHint(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	resp := runWriteTool(t, tool, ctx, WriteParams{
		FilePath: filepath.Join(t.TempDir(), "file.txt"),
		Content:  "x",
	})
	require.True(t, resp.IsError)

	idx := indexOf(resp.Content, sandbox.Marker)
	require.NotEqual(t, -1, idx)
	require.Contains(t, resp.Content, "mode=workspace-write")
	require.Contains(t, resp.Content, "hint=sandbox escalation is available")
	require.Greater(t, indexOf(resp.Content, "hint="), idx,
		"the hint must follow the marker")
}

// TestEscalation_UnavailableWithoutApprovalChannel verifies escalation is
// unavailable, not silently ignored, when there is no permission service.
func TestEscalation_UnavailableWithoutApprovalChannel(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	tool := NewWriteTool(nil, nil, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{})
	ctx := sandboxTestContext(t, workspace)

	resp := runWriteTool(t, tool, ctx, WriteParams{
		FilePath: filepath.Join(t.TempDir(), "file.txt"),
		Content:  "x",
		EscalationParams: EscalationParams{
			SandboxPermissions: "danger-full-access",
			Justification:      "Need to write outside.",
		},
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "escalation-unavailable")
}

// TestEscalation_ShellTool verifies the bash tool accepts the pair and rejects
// half of it.
func TestEscalation_ShellTool(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	policy := tableSandboxPolicy(t, workspace)
	perms := newEscalationService(true)
	attribution := sandboxAttribution()
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", nil)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)

	resp := runBashTool(t, tool, ctx, BashParams{
		Description: "half a pair",
		Command:     "true",
		EscalationParams: EscalationParams{
			SandboxPermissions: "danger-full-access",
		},
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "justification")

	// A valid pair widens the policy for the command.
	outside := filepath.Join(t.TempDir(), "escape.txt")
	resp = runBashTool(t, tool, ctx, BashParams{
		Description: "escalated write",
		Command:     "echo hi > " + outside,
		EscalationParams: EscalationParams{
			SandboxPermissions: "danger-full-access",
			Justification:      "Writing one file outside the workspace.",
		},
	})
	require.False(t, resp.IsError)
	content, err := readFile(outside)
	require.NoError(t, err)
	require.Equal(t, "hi\n", string(content))
	require.Equal(t, 1, perms.requestCount, "the escalation prompt is the only prompt")
}

// TestEscalation_ToolDescriptionsStateEscalation verifies the confined tools
// tell the model the sandbox is active and how to escalate.
func TestEscalation_ToolDescriptionsStateEscalation(t *testing.T) {
	t.Parallel()
	workspace := t.TempDir()
	attribution := sandboxAttribution()
	tools := []fantasy.AgentTool{
		NewBashTool(&mockPermissionService{}, workspace, workspace, attribution, "test", nil),
		NewWriteTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{}),
		NewEditTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{}),
		NewMultiEditTool(nil, &mockPermissionService{}, &mockHistoryService{}, mockFileTrackerService{}, workspace, FileWriteOptions{}),
		NewDownloadTool(&mockPermissionService{}, workspace, nil),
	}
	for _, tool := range tools {
		desc := tool.Info().Description
		require.Contains(t, desc, "<sandbox>", tool.Info().Name)
		require.Contains(t, desc, "sandbox_permissions", tool.Info().Name)
		require.Contains(t, desc, "justification", tool.Info().Name)
		require.Contains(t, desc, "Reads are not confined", tool.Info().Name)
	}
}

// TestEscalation_SessionGrantNeverApplies verifies "allow for session" cannot
// pre-approve an escalation.
func TestEscalation_SessionGrantNeverApplies(t *testing.T) {
	t.Parallel()
	svc := permission.NewPermissionService(t.TempDir(), false, nil)
	t.Cleanup(func() { svc.SetSkipRequests(false) })

	// Record a session grant for the escalation key.
	key := permission.PermissionKey{
		SessionID: "s",
		ToolName:  WriteToolName,
		Action:    "escalate",
		Path:      t.TempDir(),
	}
	svc.GrantPersistent(permission.PermissionRequest{
		ID:        "none",
		SessionID: key.SessionID,
		ToolName:  key.ToolName,
		Action:    key.Action,
		Path:      key.Path,
	})

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
	// A cancelled context proves the request reached the prompt path (which
	// blocks) rather than the session grant (which returns immediately).
	granted, err := svc.Request(ctx, permission.CreatePermissionRequest{
		SessionID:  "s",
		ToolName:   WriteToolName,
		Action:     "escalate",
		Path:       key.Path,
		Escalation: true,
	})
	require.Error(t, err, "an escalation must prompt, not be resolved by a session grant")
	require.False(t, granted)
}

// statPath reports file info for the assertions above.
func statPath(path string) (os.FileInfo, error) { return os.Stat(path) }

// isNotExist reports whether err is a not-exist error.
func isNotExist(err error) bool { return os.IsNotExist(err) }

// readFile reads a file for the assertions above.
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

// indexOf is strings.Index, kept local so the test reads cleanly.
func indexOf(haystack, needle string) int { return strings.Index(haystack, needle) }
