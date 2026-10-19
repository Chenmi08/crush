package tools

import (
	"context"

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

// requiresApproval answers the single remaining approval question: does this
// operation leave the sandbox's boundary? Under a confined policy only
// network-capable operations do; under danger-full-access the user has
// delegated fully and nothing prompts. With no policy attached (hooks,
// diagnostics) there is no boundary to leave and nothing prompts.
func requiresApproval(policy *sandbox.Policy, leavesBoundary bool) bool {
	if policy == nil || policy.Mode == sandbox.ModeDangerFullAccess {
		return false
	}
	return leavesBoundary
}

// escalationApprovalKey marks a context whose tool call was approved to leave
// the sandbox. It carries the tool call ID so the approval cannot leak to
// another call.
type escalationApprovalKey struct{}

// withEscalationApproval records that this exact tool call was approved to
// widen the sandbox.
func withEscalationApproval(ctx context.Context, toolCallID string) context.Context {
	return context.WithValue(ctx, escalationApprovalKey{}, toolCallID)
}

// escalationApproved reports whether this exact tool call carries an
// escalation approval.
func escalationApproved(ctx context.Context, toolCallID string) bool {
	if toolCallID == "" {
		return false
	}
	approved, _ := ctx.Value(escalationApprovalKey{}).(string)
	return approved == toolCallID
}

// requestPermission asks the permission service for approval, unless this
// exact call already obtained one by escalating the sandbox. The escalation
// prompt is the approval for the operation; asking twice would be noise and
// would let a second prompt silently reverse a user's decision.
func requestPermission(ctx context.Context, permissions permission.Service, req permission.CreatePermissionRequest) (bool, error) {
	if escalationApproved(ctx, req.ToolCallID) {
		return true, nil
	}
	return permissions.Request(ctx, req)
}

// spillDirForContext returns the directory Crush should spill oversized tool
// output into under the current policy: the policy's private temp directory,
// which the fence allows, or "" when no policy is attached (the caller keeps
// its default).
func spillDirForContext(ctx context.Context) string {
	policy := SandboxPolicyFromContext(ctx)
	if policy == nil || policy.TempDir == "" {
		return ""
	}
	return policy.TempDir
}

// sandboxedRequest marks a permission request as sandboxed or unconfined.
// The prompt states the posture; the value is informational. A
// danger-full-access policy is full delegation: the operation runs without
// confinement, and the prompt must say so rather than imply a boundary.
func sandboxedRequest(ctx context.Context, req permission.CreatePermissionRequest) permission.CreatePermissionRequest {
	policy := SandboxPolicyFromContext(ctx)
	if policy == nil {
		return req
	}
	req.Sandboxed = policy.Mode != sandbox.ModeDangerFullAccess
	req.SandboxMode = string(policy.Mode)
	return req
}
