package agent

import (
	"fmt"
	"log/slog"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/sandbox"
)

// SetSandboxMode switches the workspace's sandbox policy at runtime.
//
// Tightening takes effect immediately: every subsequent tool call on every
// agent runs under the narrower policy. Widening requires explicit approval,
// so the caller passes an approved flag (the TUI confirms with its
// "Leave Sandbox?" dialog before calling with widening modes).
//
// The new policy is derived from the store's configuration with the override
// applied, so a later restart resolves the same mode from the override alone.
func (c *coordinator) SetSandboxMode(mode sandbox.Mode, approved bool) error {
	c.agentMu.Lock()
	defer c.agentMu.Unlock()

	current := c.sandboxRuntime.RequestedMode
	if mode == current {
		return nil
	}
	if sandbox.IsWiderMode(mode, current) && !approved {
		return fmt.Errorf("widening the sandbox from %s to %s requires approval", current, mode)
	}

	// The override makes the choice survive config reloads and restarts.
	// The previous values are captured so a failed resolution can restore
	// exactly what was there, not what the current mode happens to be (they
	// differ when the effective mode came from config rather than an
	// override).
	previousMode := c.cfg.Overrides().SandboxMode
	previousDisable := c.cfg.Overrides().DisableSandbox
	c.cfg.Overrides().SandboxMode = string(mode)
	c.cfg.Overrides().DisableSandbox = mode == sandbox.ModeDangerFullAccess
	if mode == sandbox.ModeDangerFullAccess {
		warnSandboxDisabledByCommand()
	}

	rt := c.cfg.ResolveSandboxRuntime()
	if rt.Err != nil {
		c.cfg.Overrides().SandboxMode = previousMode
		c.cfg.Overrides().DisableSandbox = previousDisable
		return fmt.Errorf("sandbox mode %s could not be resolved: %w", mode, rt.Err)
	}
	c.sandboxRuntime = rt

	// Every agent built by this coordinator shares the policy; update them
	// all so sub-agents do not keep running under the old boundary.
	for _, agent := range c.agents {
		agent.SetSandboxPolicy(rt.Policy, rt.Configuration.ExcludeCommands)
	}

	slog.Info("Sandbox mode changed", "from", current, "to", mode, "enforcement", c.cfg.SandboxStatus(rt).Enforcement)
	return nil
}

// SandboxMode returns the mode currently in effect for this workspace.
func (c *coordinator) SandboxMode() sandbox.Mode {
	c.agentMu.RLock()
	defer c.agentMu.RUnlock()
	return c.sandboxRuntime.RequestedMode
}

// SandboxStatus renders the resolved policy and enforcement facts.
func (c *coordinator) SandboxStatus() config.SandboxRuntime {
	c.agentMu.RLock()
	defer c.agentMu.RUnlock()
	return c.sandboxRuntime
}

// warnSandboxDisabledByCommand prints the visible warning when the sandbox is
// turned off from inside a session, matching the startup warning's wording.
func warnSandboxDisabledByCommand() {
	fmtWarnOnce.Do(func() {
		fmtWarn("WARNING: the sandbox is disabled. Tool writes are not confined; the agent has the full authority of your user account.\n")
	})
}
