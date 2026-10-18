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

// TestSetSandboxMode_UpdatesAllAgents verifies main agents observe the new
// policy, while a built sub-agent keeps its derived read-only policy: the
// read-only mode and its write roots are pinned, and the workspace, temp
// directory, and protected set are invariant across mode switches.
func TestSetSandboxMode_UpdatesAllAgents(t *testing.T) {
	c := sandboxTestCoordinator(t)
	main := &mockSessionAgent{}
	c.agents["coder"] = main

	require.NoError(t, c.SetSandboxMode(sandbox.ModeReadOnly, false))
	require.NotNil(t, main.sandboxPolicy)
	require.Equal(t, sandbox.ModeReadOnly, main.sandboxPolicy.Mode)

	// Widening the workspace leaves a built sub-agent confined.
	require.NoError(t, c.SetSandboxMode(sandbox.ModeWorkspaceWrite, true))
	require.Equal(t, sandbox.ModeWorkspaceWrite, main.sandboxPolicy.Mode)
	require.Equal(t, sandbox.ModeWorkspaceWrite, c.sandboxRuntime.Policy.Mode)

	sub := sandbox.SubagentPolicy(c.sandboxRuntime.Policy)
	require.Equal(t, sandbox.ModeReadOnly, sub.Mode,
		"a sub-agent must not inherit the widened workspace mode")
	require.Equal(t, []string{sub.TempDir}, sub.WriteRoots,
		"a sub-agent's only writable root is its private temp directory")
	require.NoError(t, sub.CheckWrite(sub.TempDir+"/spill.txt"))
	require.Error(t, sub.CheckWrite(sub.Workspace+"/file.go"),
		"the widened workspace stays unwritable for sub-agents")
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
