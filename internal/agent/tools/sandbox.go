package tools

import (
	"context"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/sandbox"
)

// sandboxContextKey carries the resolved policy for the current agent turn.
type sandboxContextKey struct{}

// WithSandboxPolicy attaches the resolved sandbox policy to a context. It is
// called by the agent runtime when a turn starts, so every tool invoked for
// that turn sees the same policy without the policy being stored globally.
func WithSandboxPolicy(ctx context.Context, policy *sandbox.Policy) context.Context {
	return context.WithValue(ctx, sandboxContextKey{}, policy)
}

// SandboxPolicyFromContext returns the policy attached to ctx, or nil when
// no policy applies (tests, hooks, non-session callers). A nil policy means
// "no fence": callers keep their existing behaviour.
func SandboxPolicyFromContext(ctx context.Context) *sandbox.Policy {
	policy, _ := ctx.Value(sandboxContextKey{}).(*sandbox.Policy)
	return policy
}

// checkSandboxWrite refuses a mutating tool call whose target is outside the
// policy's writable set. It must run before any directory is created so a
// refused write leaves no filesystem trace.
//
// The refusal is a tool error carrying the shared denial marker, so the model
// sees the same vocabulary a kernel denial would produce.
func checkSandboxWrite(ctx context.Context, toolName, path string) fantasy.ToolResponse {
	policy := SandboxPolicyFromContext(ctx)
	if policy == nil {
		return fantasy.ToolResponse{}
	}
	err := policy.CheckWrite(path)
	if err == nil {
		return fantasy.ToolResponse{}
	}
	d, ok := sandbox.AsDenial(err)
	if !ok {
		return fantasy.ToolResponse{}
	}
	d.Tool = toolName
	d.EscalationHint = true
	sandbox.LogDenial(d)
	resp := fantasy.NewTextErrorResponse(d.Error())
	// A policy denial is a hard stop for this call: retrying with another
	// tool cannot succeed because the boundary is shared.
	resp.StopTurn = true
	return resp
}

// sandboxedRequest marks a permission request as sandboxed or unsandboxed.
// Approval semantics depend on it: a confined command is auto-approved, an
// excluded or unconfined one prompts.
func sandboxedRequest(ctx context.Context, req permission.CreatePermissionRequest) permission.CreatePermissionRequest {
	policy := SandboxPolicyFromContext(ctx)
	if policy == nil {
		return req
	}
	req.Sandboxed = policy.Mode != sandbox.ModeDangerFullAccess
	req.SandboxMode = string(policy.Mode)
	return req
}
