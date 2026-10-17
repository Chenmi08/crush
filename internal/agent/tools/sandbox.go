package tools

import (
	"context"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/sandbox"
)

// sandboxContextKey carries the resolved policy for the current agent turn.
type sandboxContextKey struct{}

// exclusionsContextKey carries the configured command exclusions for the
// current agent turn.
type exclusionsContextKey struct{}

// WithSandboxExclusions attaches the configured command exclusions to a
// context. They travel with the policy so one execution cannot observe
// another's settings.
func WithSandboxExclusions(ctx context.Context, patterns []string) context.Context {
	return context.WithValue(ctx, exclusionsContextKey{}, patterns)
}

// ExclusionPatternsFromContext returns the exclusions attached to ctx.
func ExclusionPatternsFromContext(ctx context.Context) []string {
	patterns, _ := ctx.Value(exclusionsContextKey{}).([]string)
	return patterns
}

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
	resp := fantasy.NewTextErrorResponse(escalationDenialHint(d))
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

// commandPolicy is the per-command result of applying the session policy.
type commandPolicy struct {
	// Policy is the policy the command actually runs under. Nil means no
	// policy is attached at all, which keeps the caller's previous
	// behaviour.
	Policy *sandbox.Policy
	// Sandboxed reports whether the boundary applies to this command.
	Sandboxed bool
	// Reason explains why the command is not sandboxed, for the prompt.
	Reason string
}

// sandboxPolicyForCommand resolves the policy that applies to one shell
// command. Exclusions remove the boundary and keep the approval flow; with no
// session policy, or an explicitly disabled sandbox, the command runs
// unconfined and prompts.
func sandboxPolicyForCommand(ctx context.Context, command string) commandPolicy {
	policy := SandboxPolicyFromContext(ctx)
	if policy == nil {
		return commandPolicy{}
	}
	if policy.Mode == sandbox.ModeDangerFullAccess {
		return commandPolicy{Policy: policy, Reason: "sandbox disabled"}
	}
	if sandbox.IsSandboxExcludedCommand(command, ExclusionPatternsFromContext(ctx)) {
		return commandPolicy{Policy: policy, Reason: "command excluded from the sandbox"}
	}
	// A confined mode with no usable backend, or with a backend that only
	// fences Crush's own writes, must prompt rather than run silently: the
	// command's child processes would be unconfined.
	if status := sandbox.Describe(ctx, policy); status.Backend == "none" || !status.ConfinesChildren {
		reason := "no sandbox backend is available"
		if status.Backend != "none" {
			reason = status.Missing
		}
		return commandPolicy{Policy: policy, Reason: reason}
	}
	return commandPolicy{Policy: policy, Sandboxed: true}
}

// RequiresApproval reports whether this command must go through the
// permission flow. A confined command does not; an excluded, unconfined, or
// unavailable-backend command does, even when it would otherwise match the
// read-only shortcut. A configured dangerous command always does, even when
// confined, so grants like `git push` are never granted implicitly. With no
// policy attached at all, the caller's own read-only decision applies.
func (c commandPolicy) RequiresApproval(safeReadOnly, dangerous bool) bool {
	if dangerous {
		return true
	}
	if c.Policy == nil {
		return !safeReadOnly
	}
	if c.Sandboxed {
		return false
	}
	return true
}

// SandboxState describes the request for the prompt and audit subscribers.
func (c commandPolicy) SandboxState() (sandboxed bool, mode string) {
	if c.Policy == nil {
		return false, ""
	}
	return c.Sandboxed, string(c.Policy.Mode)
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
