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

// sandboxPermissionsDescription is the parameter description shared by every
// confined tool, so the model learns one rule. It states that the sandbox is
// active and how to escalate.
const sandboxPermissionsDescription = "Request a wider sandbox mode for this one call only. Requires justification. The user is asked to approve leaving the sandbox; approval never carries to another call. Accepted: read-only, workspace-write, danger-full-access"

// justificationDescription pairs with sandbox_permissions.
const justificationDescription = "A sentence explaining why this call needs a wider sandbox mode. Required whenever sandbox_permissions is set, and ignored when it is not"

// EscalationParams are embedded in every confined tool's parameter struct.
type EscalationParams struct {
	SandboxPermissions string `json:"sandbox_permissions,omitempty" description:"Request a wider sandbox mode for this one call only. Requires justification. The user is asked to approve leaving the sandbox; approval never carries to another call. Accepted: read-only, workspace-write, danger-full-access"`
	Justification      string `json:"justification,omitempty" description:"A sentence explaining why this call needs a wider sandbox mode. Required whenever sandbox_permissions is set, and ignored when it is not"`
}

// escalationRequest is a validated single-call escalation.
type escalationRequest struct {
	mode          sandbox.Mode
	justification string
}

// parseEscalation validates the pair. Both must be present or neither: an
// escalation without a justification, or a justification without a mode, is
// malformed and rejected before execution.
func parseEscalation(params EscalationParams) (*escalationRequest, error) {
	mode := strings.TrimSpace(params.SandboxPermissions)
	justification := strings.TrimSpace(params.Justification)

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
func resolveEscalation(
	ctx context.Context,
	permissions permission.Service,
	toolName string,
	req *escalationRequest,
	effective *sandbox.Policy,
	call fantasy.ToolCall,
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

	granted, err := permissions.Request(ctx, permission.CreatePermissionRequest{
		SessionID:      sessionID,
		ToolCallID:     call.ID,
		ToolName:       toolName,
		Action:         "escalate",
		Description:    fmt.Sprintf("Leave the sandbox for one call (%s → %s): %s", effective.Mode, req.mode, req.justification),
		Path:           effective.Workspace,
		Params:         req,
		Escalation:     true,
		EscalationMode: string(req.mode),
		Sandboxed:      false,
	})
	if err != nil {
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
) (context.Context, *fantasy.ToolResponse, error) {
	if req == nil {
		return ctx, nil, nil
	}
	effective := SandboxPolicyFromContext(ctx)
	policy, resp, err := resolveEscalation(ctx, permissions, toolName, req, effective, call)
	if err != nil {
		return ctx, nil, err
	}
	if resp.Content != "" || resp.IsError {
		return ctx, &resp, nil
	}
	ctx = withEscalationApproval(ctx, call.ID)
	return WithSandboxPolicy(ctx, policy), nil, nil
}
