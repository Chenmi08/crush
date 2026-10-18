package tools

import (
	"context"
	"errors"
	"fmt"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/sandbox"
)

// escalationRequest is a validated single-call escalation.
type escalationRequest struct {
	mode          sandbox.Mode
	justification string
}

// parseEscalation validates the sandbox_permissions/justification pair. Both
// must be present or neither: an escalation without a justification, or a
// justification without a mode, is malformed and rejected before execution.
func parseEscalation(sandboxPermissions, justification string) (*escalationRequest, error) {
	mode := strings.TrimSpace(sandboxPermissions)
	justification = strings.TrimSpace(justification)

	if mode == "" && justification == "" {
		return nil, nil
	}
	if mode == "" {
		return nil, errors.New("justification requires sandbox_permissions; set both or neither")
	}
	if justification == "" {
		return nil, errors.New("sandbox_permissions requires a justification sentence; set both or neither")
	}
	parsed, err := sandbox.ValidateMode(mode)
	if err != nil {
		return nil, err
	}
	return &escalationRequest{mode: parsed, justification: justification}, nil
}

// escalationDeniedResponse is the model-facing result of a rejected
// escalation. It instructs the model to stop and explain rather than work
// around the boundary with another tool.
func escalationDeniedResponse(mode sandbox.Mode) fantasy.ToolResponse {
	resp := fantasy.NewTextErrorResponse(fmt.Sprintf(
		"%s mode=%s reason=escalation-denied hint=stop and explain to the user that this operation needs a wider sandbox mode; do not retry with another tool, and do not work around the boundary",
		sandbox.Marker, mode))
	resp.StopTurn = true
	return resp
}

// escalationUnavailableResponse is returned when there is no approval channel
// or no agent to route the request through. Escalation is unavailable rather
// than silently ignored.
func escalationUnavailableResponse() fantasy.ToolResponse {
	resp := fantasy.NewTextErrorResponse(fmt.Sprintf(
		"%s reason=escalation-unavailable hint=sandbox escalation requires an interactive session with a user to approve it; continue without escalation or stop and explain",
		sandbox.Marker))
	resp.StopTurn = true
	return resp
}

// escalationDenialHint returns the shared denial text for a policy refusal,
// including the escalation hint when the caller may request a wider mode.
func escalationDenialHint(d sandbox.Denial) string {
	d.EscalationHint = true
	return d.Error()
}

// resolveEscalation validates an escalation request against the call's
// effective policy and, when valid, asks the user for a single-call approval.
// It returns the policy the call should use.
//
// The check that the requested mode is strictly wider than the effective mode
// happens here, at execution time, not in the schema: the effective mode is
// per-call while schemas are global.
//
// dialogParams is what the approval dialog should render besides the
// escalation rows: the tool's own permission parameters (the command, the
// file path, the diff). Leaving it carries the escalation request itself,
// which the default panel would print as an opaque struct; a nil value falls
// back to that so callers without tool params still work.
func resolveEscalation(
	ctx context.Context,
	permissions permission.Service,
	toolName string,
	req *escalationRequest,
	effective *sandbox.Policy,
	call fantasy.ToolCall,
	dialogParams any,
) (*sandbox.Policy, fantasy.ToolResponse, error) {
	if req == nil {
		return effective, fantasy.ToolResponse{}, nil
	}
	if effective == nil {
		return effective, escalationUnavailableResponse(), nil
	}
	if !sandbox.IsWiderMode(req.mode, effective.Mode) {
		return effective, fantasy.NewTextErrorResponse(fmt.Sprintf(
			"%s mode=%s reason=escalation-not-wider hint=the requested mode must be strictly wider than the mode in effect; no approval was requested",
			sandbox.Marker, effective.Mode)), nil
	}
	if permissions == nil || call.ID == "" {
		return effective, escalationUnavailableResponse(), nil
	}

	sessionID := GetSessionFromContext(ctx)
	if sessionID == "" {
		return effective, escalationUnavailableResponse(), nil
	}

	params := any(req)
	if dialogParams != nil {
		params = dialogParams
	}

	granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:      sessionID,
		ToolCallID:     call.ID,
		ToolName:       toolName,
		Action:         "escalate",
		Description:    fmt.Sprintf("Leave the sandbox for one call (%s → %s): %s", effective.Mode, req.mode, req.justification),
		Path:           effective.Workspace,
		Params:         params,
		Escalation:     true,
		EscalationMode: string(req.mode),
		Reasoning:      req.justification,
		Sandboxed:      false,
	})
	if err != nil {
		if errors.Is(err, permission.ErrApprovalUnavailable) {
			return effective, escalationUnavailableResponse(), nil
		}
		return effective, fantasy.ToolResponse{}, err
	}
	if !granted {
		return effective, escalationDeniedResponse(req.mode), nil
	}

	// The approval applies to exactly this call. The wider policy is built
	// for this execution only and is never recorded as a session grant.
	widened, err := escalatePolicy(effective, req.mode)
	if err != nil {
		return effective, fantasy.NewTextErrorResponse(err.Error()), nil
	}
	return widened, fantasy.ToolResponse{}, nil
}

// nextWiderMode returns the narrowest mode strictly wider than m. It is what
// an automatic fence escalation asks for: enough to allow the write, no more.
func nextWiderMode(m sandbox.Mode) sandbox.Mode {
	for _, candidate := range sandbox.Modes() {
		if sandbox.IsWiderMode(candidate, m) {
			return candidate
		}
	}
	return sandbox.ModeDangerFullAccess
}

// escalatePolicy builds the wider policy for a single call. It is derived from
// the effective policy so the workspace, protected set, and private temp
// directory stay the same; only the mode changes.
func escalatePolicy(effective *sandbox.Policy, mode sandbox.Mode) (*sandbox.Policy, error) {
	if effective == nil {
		return nil, errors.New("no sandbox policy to escalate from")
	}
	if !sandbox.IsWiderMode(mode, effective.Mode) {
		return nil, errors.New("escalation mode must be strictly wider than the effective mode")
	}
	widened := *effective
	widened.Mode = mode
	// A wider mode widens the writable set to what that mode grants. The
	// workspace, protected set, temp directory, and cache stay the same, so
	// protected paths remain protected after an escalation.
	switch mode {
	case sandbox.ModeWorkspaceWrite:
		roots := []string{widened.Workspace}
		if widened.TempDir != "" {
			roots = append(roots, widened.TempDir)
		}
		if widened.CacheDir != "" {
			roots = append(roots, widened.CacheDir)
		}
		widened.WriteRoots = sandbox.UniquePaths(roots)
	case sandbox.ModeReadOnly:
		if widened.TempDir != "" {
			widened.WriteRoots = []string{widened.TempDir}
		} else {
			widened.WriteRoots = nil
		}
	}
	return &widened, nil
}

// applyEscalation resolves an escalation for a non-shell tool and returns the
// context carrying the (possibly widened) policy. A non-nil response means the
// call must return that response and not proceed.
//
// The permission service is optional: the file tools treat a nil service as
// "no approval channel", in which case escalation is unavailable rather than
// silently ignored.
func applyEscalation(
	ctx context.Context,
	permissions permission.Service,
	toolName string,
	req *escalationRequest,
	call fantasy.ToolCall,
	dialogParams any,
) (context.Context, *fantasy.ToolResponse, error) {
	if req == nil {
		return ctx, nil, nil
	}
	effective := SandboxPolicyFromContext(ctx)
	policy, resp, err := resolveEscalation(ctx, permissions, toolName, req, effective, call, dialogParams)
	if err != nil {
		return ctx, nil, err
	}
	if resp.Content != "" || resp.IsError {
		return ctx, &resp, nil
	}
	ctx = withEscalationApproval(ctx, call.ID)
	return WithSandboxPolicy(ctx, policy), nil, nil
}

// fenceEscalationResponse is what a file tool does when the fence refuses a
// write: ask the user once, via the same "Leave Sandbox?" dialog an explicit
// escalation uses. Approved, the wider policy applies to exactly this call
// and the tool proceeds; denied, the original denial is returned so the model
// sees the standard refusal.
//
// Only the idempotent file tools route through here (write, edit, multiedit,
// download): replaying the same call after approval cannot double-apply
// anything. The bash tool keeps its model-initiated escalation, because a
// shell command may have partially run before the refusal and replaying it is
// not safe.
//
// The requested mode is one step wider, not the widest: a refusal to write
// outside the writable roots is answered by workspace-write (which keeps the
// protected set and the temp directory), and only a refusal from read-only
// needs workspace-write's broader roots. Asking for danger-full-access on
// every out-of-tree write would hand over the whole disk to answer a much
// smaller question.
func fenceEscalation(
	ctx context.Context,
	permissions permission.Service,
	toolName string,
	d sandbox.Denial,
	call fantasy.ToolCall,
	dialogParams any,
) (context.Context, *fantasy.ToolResponse, error) {
	effective := SandboxPolicyFromContext(ctx)
	if effective == nil {
		return ctx, nil, nil
	}
	req := &escalationRequest{
		mode: nextWiderMode(effective.Mode),
		justification: fmt.Sprintf(
			"The sandbox refused a write to %s (mode %s). Approving lets this one %s call write the file under %s; denying sends the refusal back to the model, which can ask you differently or use a path inside the workspace.",
			d.Path, d.Mode, toolName, nextWiderMode(effective.Mode)),
	}
	return applyEscalation(ctx, permissions, toolName, req, call, dialogParams)
}

// handleFenceDenial is the shared fence exit for the file tools. When the
// policy refuses the write it asks the user once; the returned context
// carries the widened policy when the user approved, so the caller proceeds
// with its normal path. A nil response means "continue".
func handleFenceDenial(
	ctx context.Context,
	permissions permission.Service,
	toolName, path string,
	call fantasy.ToolCall,
	dialogParams any,
) (context.Context, *fantasy.ToolResponse, error) {
	policy := SandboxPolicyFromContext(ctx)
	if policy == nil {
		return ctx, nil, nil
	}
	err := policy.CheckWrite(path)
	if err == nil {
		return ctx, nil, nil
	}
	d, ok := sandbox.AsDenial(err)
	if !ok {
		return ctx, nil, nil
	}
	d.Tool = toolName
	sandbox.LogDenial(d)

	// A protected path cannot be re-allowed by any setting, and an invalid
	// configuration is not fixed by widening the mode, so asking would be
	// theater: refuse outright with the escalation hint.
	if d.Protected || d.ConfigError {
		d.EscalationHint = false
		resp := fantasy.NewTextErrorResponse(d.Error())
		resp.StopTurn = true
		return ctx, &resp, nil
	}

	return fenceEscalation(ctx, permissions, toolName, d, call, dialogParams)
}
