package agent

import (
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/stretchr/testify/require"
)

// sandboxTestCoordinator builds a minimal coordinator whose sandbox runtime
// is resolved, exercising the runtime mode switch without an LLM.
func sandboxTestCoordinator(t *testing.T) *coordinator {
	t.Helper()
	cfg, err := config.Init(t.TempDir(), "", false)
	require.NoError(t, err)
	c := &coordinator{cfg: cfg, agents: make(map[string]SessionAgent)}
	c.sandboxRuntime = cfg.ResolveSandboxRuntime()
	require.NoError(t, c.sandboxRuntime.Err)
	// The default mode with a usable backend; on hosts without one the
	// policy still exists and tightening semantics are unchanged.
	require.NotNil(t, c.sandboxRuntime.Policy)
	return c
}

// TestSetSandboxMode_TightenAppliesImmediately verifies tightening needs no
// approval, swaps the policy on every agent, and records the override.
func TestSetSandboxMode_TightenAppliesImmediately(t *testing.T) {
	c := sandboxTestCoordinator(t)
	current := c.SandboxMode()
	t.Cleanup(func() { _ = c.SetSandboxMode(current, true) })

	require.NoError(t, c.SetSandboxMode(sandbox.ModeReadOnly, false))
	require.Equal(t, sandbox.ModeReadOnly, c.SandboxMode())
	require.NotNil(t, c.SandboxStatus().Policy)
	require.Equal(t, sandbox.ModeReadOnly, c.SandboxStatus().Policy.Mode)
	require.Equal(t, "read-only", c.cfg.Overrides().SandboxMode,
		"the override must survive config reloads and restarts")
}

// TestSetSandboxMode_WidenRequiresApproval verifies a widening without
// approval is refused and changes nothing.
func TestSetSandboxMode_WidenRequiresApproval(t *testing.T) {
	c := sandboxTestCoordinator(t)
	c.sandboxRuntime.RequestedMode = sandbox.ModeReadOnly

	err := c.SetSandboxMode(sandbox.ModeWorkspaceWrite, false)
	require.Error(t, err)
	require.Contains(t, err.Error(), "requires approval")
	require.Equal(t, sandbox.ModeReadOnly, c.SandboxMode(),
		"a refused widening leaves the policy untouched")
	require.NotEqual(t, "workspace-write", c.cfg.Overrides().SandboxMode)

	// With approval the same call succeeds.
	require.NoError(t, c.SetSandboxMode(sandbox.ModeWorkspaceWrite, true))
	require.Equal(t, sandbox.ModeWorkspaceWrite, c.SandboxMode())
}

// TestSetSandboxMode_SameModeIsNoop verifies switching to the current mode is
// a no-op, not an error.
func TestSetSandboxMode_SameModeIsNoop(t *testing.T) {
	c := sandboxTestCoordinator(t)
	mode := c.SandboxMode()
	require.NoError(t, c.SetSandboxMode(mode, false))
	require.Equal(t, mode, c.SandboxMode())
}

// TestSetSandboxMode_InvalidModeRejected verifies an unresolvable mode leaves
// the previous override in place.
func TestSetSandboxMode_InvalidModeRejected(t *testing.T) {
	c := sandboxTestCoordinator(t)
	before := c.cfg.Overrides().SandboxMode
	err := c.SetSandboxMode(sandbox.Mode("yolo"), false)
	require.Error(t, err)
	require.Equal(t, before, c.cfg.Overrides().SandboxMode,
		"a failed resolution must not leave a broken override behind")
}

// TestSetSandboxMode_UpdatesAllAgents verifies sub-agents do not keep running
// under the old boundary.
func TestSetSandboxMode_UpdatesAllAgents(t *testing.T) {
	c := sandboxTestCoordinator(t)
	main := &mockSessionAgent{}
	sub := &mockSessionAgent{}
	c.agents["coder"] = main
	c.agents["task"] = sub

	require.NoError(t, c.SetSandboxMode(sandbox.ModeReadOnly, false))
	require.NotNil(t, main.sandboxPolicy)
	require.Equal(t, sandbox.ModeReadOnly, main.sandboxPolicy.Mode)
	require.Equal(t, main.sandboxPolicy, sub.sandboxPolicy,
		"every agent must observe the same policy object")
}

// TestSetSandboxMode_DangerRequiresApproval verifies danger-full-access is
// never reachable without the same confirmation a widening needs.
func TestSetSandboxMode_DangerRequiresApproval(t *testing.T) {
	c := sandboxTestCoordinator(t)
	t.Cleanup(func() { _ = c.SetSandboxMode(sandbox.ModeWorkspaceWrite, true) })

	err := c.SetSandboxMode(sandbox.ModeDangerFullAccess, false)
	require.Error(t, err)
	require.Equal(t, sandbox.ModeWorkspaceWrite, c.SandboxMode())
}
