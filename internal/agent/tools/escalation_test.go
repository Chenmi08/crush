package tools

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"reflect"
	"strings"
	"testing"

	"charm.land/fantasy"
	"charm.land/fantasy/schema"
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

	_, err := parseEscalation("danger-full-access", "")
	require.Error(t, err)
	require.Contains(t, err.Error(), "justification")

	_, err = parseEscalation("", "I need to write outside.")
	require.Error(t, err)
	require.Contains(t, err.Error(), "sandbox_permissions")

	_, err = parseEscalation("bogus", "because")
	require.Error(t, err)
	require.Contains(t, err.Error(), "accepted")

	req, err := parseEscalation(
		"danger-full-access",
		"Writing the release artefact outside the repository.")
	require.NoError(t, err)
	require.Equal(t, sandbox.ModeDangerFullAccess, req.mode)

	none, err := parseEscalation("", "")
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
		// Missing justification.
		SandboxPermissions: "danger-full-access",
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "justification")
	_, err := os.Stat(target)
	require.True(t, os.IsNotExist(err))
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
		FilePath:           outside,
		Content:            "x",
		SandboxPermissions: "workspace-write",
		Justification:      "Same mode, not wider.",
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
		FilePath:           outside,
		Content:            "x",
		SandboxPermissions: "danger-full-access",
		Justification:      "Writing outside the workspace once.",
	})
	require.False(t, resp.IsError, "the approved escalation must let the call proceed")
	content, err := os.ReadFile(outside)
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
	_, statErr := os.Stat(second)
	require.True(t, os.IsNotExist(statErr))
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
		FilePath:           outside,
		Content:            "x",
		SandboxPermissions: "danger-full-access",
		Justification:      "Need to write outside.",
	})
	require.True(t, resp.IsError)
	require.True(t, resp.StopTurn, "the turn must stop so the model does not retry another route")
	require.Contains(t, resp.Content, "escalation-denied")
	require.Contains(t, resp.Content, "do not retry")
	_, err := os.Stat(outside)
	require.True(t, os.IsNotExist(err))
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
		FilePath:           filepath.Join(t.TempDir(), "x"),
		Content:            "x",
		SandboxPermissions: "danger-full-access",
		Justification:      "Need to write outside.",
	})
	require.NotNil(t, perms.lastRequest)
	require.True(t, perms.lastRequest.Escalation)
	require.Equal(t, "danger-full-access", perms.lastRequest.EscalationMode)
	require.False(t, perms.lastRequest.Sandboxed,
		"the requested operation is not sandboxed, which the prompt must state")
	require.Contains(t, perms.lastRequest.Description, "Leave the sandbox")
	require.Equal(t, "Need to write outside.", perms.lastRequest.Reasoning,
		"the model's justification must reach the dialog for the user to read")
}

// TestEscalation_DenialCarriesHint verifies the model-facing denial carries
// the mode marker and, for the bash tool (whose refusals stay model-initiated),
// an escalation hint in shared wording. The file tools now raise the dialog on
// refusal (see TestWriteTool_SandboxRefusalAsksOnce), so this covers the bash
// path through the interpreter fence via the policy directly.
func TestEscalation_DenialCarriesHint(t *testing.T) {
	t.Parallel()
	policy := tableSandboxPolicy(t, t.TempDir())
	err := policy.CheckWrite(filepath.Join(t.TempDir(), "file.txt"))
	require.Error(t, err)
	d, ok := sandbox.AsDenial(err)
	require.True(t, ok)
	d.EscalationHint = true

	msg := d.Error()
	idx := strings.Index(msg, sandbox.Marker)
	require.NotEqual(t, -1, idx)
	require.Contains(t, msg, "mode=workspace-write")
	require.Contains(t, msg, "hint=sandbox escalation is available")
	require.Greater(t, strings.Index(msg, "hint="), idx,
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
		FilePath:           filepath.Join(t.TempDir(), "file.txt"),
		Content:            "x",
		SandboxPermissions: "danger-full-access",
		Justification:      "Need to write outside.",
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
	tool := NewBashTool(perms, workspace, workspace, attribution, "test-model", nil, nil)

	ctx := context.WithValue(context.Background(), SessionIDContextKey, "test-session")
	ctx = WithSandboxPolicy(ctx, policy)

	resp := runBashTool(t, tool, ctx, BashParams{
		Description:        "half a pair",
		Command:            "true",
		SandboxPermissions: "danger-full-access",
	})
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "justification")

	// A valid pair widens the policy for the command.
	outside := filepath.Join(t.TempDir(), "escape.txt")
	resp = runBashTool(t, tool, ctx, BashParams{
		Description:        "escalated write",
		Command:            "echo hi > " + outside,
		SandboxPermissions: "danger-full-access",
		Justification:      "Writing one file outside the workspace.",
	})
	require.False(t, resp.IsError)
	content, err := os.ReadFile(outside)
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
		NewBashTool(&mockPermissionService{}, workspace, workspace, attribution, "test", nil, nil),
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

	// An attached approval surface keeps the request on the prompt path; a
	// cancelled context then proves it waited for a prompt (which blocks)
	// rather than being resolved by the session grant (which returns
	// immediately). Without a subscriber the service now fails fast instead.
	_ = svc.Subscribe(t.Context())

	ctx, cancel := context.WithCancel(t.Context())
	cancel()
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

// TestEscalation_SchemaMatchesDecoding guards against the schema and the
// decoder disagreeing on the escalation parameters' shape. The schema is what
// the model sees, so the decoder must accept exactly that shape: a flat
// sandbox_permissions/justification pair on every confined tool. When they
// diverged, the model sent a nested escalation_params object that decoding
// silently dropped, and the escalation never fired.
func TestEscalation_SchemaMatchesDecoding(t *testing.T) {
	t.Parallel()

	cases := []struct {
		params any
	}{
		{BashParams{Command: "true"}},
		{WriteParams{FilePath: "x", Content: "x"}},
		{EditParams{FilePath: "x", OldString: "x", NewString: "x"}},
		{MultiEditParams{FilePath: "x", Edits: []MultiEditOperation{}}},
		{DownloadParams{URL: "https://example.com", FilePath: "x"}},
	}
	for _, tc := range cases {
		generated := schema.Generate(reflect.TypeOf(tc.params))

		_, hasNested := generated.Properties["escalation_params"]
		require.False(t, hasNested,
			"escalation params must be flat, not nested under escalation_params")

		for _, name := range []string{"sandbox_permissions", "justification"} {
			prop, ok := generated.Properties[name]
			require.True(t, ok, "%T is missing the %s property", tc.params, name)
			require.Equal(t, "string", prop.Type, "%T %s", tc.params, name)
			require.NotEmpty(t, prop.Description, "%T %s", tc.params, name)
		}

		// The exact payload a model would send from this schema must decode
		// back into the populated fields.
		input, err := json.Marshal(map[string]any{
			"sandbox_permissions": "danger-full-access",
			"justification":       "test",
		})
		require.NoError(t, err)
		decoded := reflect.New(reflect.TypeOf(tc.params)).Interface()
		require.NoError(t, json.Unmarshal(input, decoded), "%T", tc.params)

		v := reflect.ValueOf(decoded).Elem()
		require.Equal(t, "danger-full-access",
			v.FieldByName("SandboxPermissions").String(), "%T", tc.params)
		require.Equal(t, "test",
			v.FieldByName("Justification").String(), "%T", tc.params)
	}
}
