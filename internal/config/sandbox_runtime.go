package config

import (
	"context"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sync"

	"github.com/charmbracelet/crush/internal/sandbox"
)

// SandboxRuntime holds the resolved sandbox policy for this workspace so it is
// derived exactly once and shared by the agent, the shell, and the status
// line.
type SandboxRuntime struct {
	// Policy is the resolved policy, or nil when no fence applies (for
	// example when the sandbox is disabled).
	Policy *sandbox.Policy
	// Configuration is the merged sandbox configuration that produced the
	// policy.
	Configuration Sandbox
	// RequestedMode is the mode the user asked for, even when resolution
	// failed.
	RequestedMode sandbox.Mode
	// Err is set when the requested configuration could not be resolved.
	Err error
	// Disabled reports that the sandbox was turned off explicitly.
	Disabled bool
}

// ResolveSandboxRuntime resolves the workspace's sandbox policy. It is called
// once by the app and by the diagnostics commands so every surface agrees on
// the same policy.
//
// Turning the sandbox off is a deliberate act with a visible warning: a
// danger-full-access mode (from a flag or from configuration) and the explicit
// disable flag both print one to stderr, once per process.
func (s *ConfigStore) ResolveSandboxRuntime() SandboxRuntime {
	rt := SandboxRuntime{RequestedMode: sandbox.DefaultMode}

	cfg := s.Config()
	permissions := cfg.Permissions
	requested := permissions.SandboxConfig()

	overrides := s.Overrides()
	if overrides.SandboxMode != "" {
		requested.Mode = overrides.SandboxMode
	}
	if overrides.DisableSandbox {
		rt.Disabled = true
		rt.Configuration = requested
		rt.RequestedMode = sandbox.ModeDangerFullAccess
		warnSandboxDisabled("the sandbox was disabled explicitly")
		return rt
	}

	if rt.RequestedMode = sandbox.Mode(requested.Mode); rt.RequestedMode == "" {
		rt.RequestedMode = sandbox.DefaultMode
	}

	if err := ValidateSandbox(requested); err != nil {
		rt.Err = err
		rt.Configuration = requested
		return rt
	}

	dataDir := ""
	if cfg.Options != nil {
		dataDir = cfg.Options.DataDirectory
	}

	policy, err := ResolveSandbox(requested, SandboxResolveOptions{
		Workspace:       s.WorkingDir(),
		TempDir:         DefaultSandboxTempDir(dataDir),
		CacheDir:        sandboxCacheDir(),
		GlobalConfigDir: filepath.Dir(GlobalConfig()),
		GlobalDataDir:   filepath.Dir(GlobalConfigData()),
	})
	rt.Configuration = requested
	if err != nil {
		rt.Err = err
		return rt
	}
	rt.Policy = policy
	if policy.Mode == sandbox.ModeDangerFullAccess {
		warnSandboxDisabled("sandbox mode is danger-full-access")
	}
	slog.Info("Sandbox policy resolved", "mode", policy.Mode, "roots", policy.WriteRoots, "protected", len(policy.Protected))
	return rt
}

var sandboxWarningOnce sync.Once

// warnSandboxDisabled prints the visible warning that accompanies running
// unconfined. It fires once per process so multiple policy resolutions (the
// app, the status line, diagnostics) do not repeat it.
func warnSandboxDisabled(reason string) {
	sandboxWarningOnce.Do(func() {
		fmt.Fprintf(os.Stderr, "WARNING: the sandbox is disabled (%s). Tool writes are not confined; the agent has the full authority of your user account.\n", reason)
	})
}

// sandboxCacheDir returns the user cache directory, which is writable by
// default so go build, go test, npm install, and pip install work out of the
// box.
func sandboxCacheDir() string {
	dir := GlobalCacheDir()
	if dir == "" {
		return ""
	}
	if err := os.MkdirAll(dir, 0o700); err != nil {
		slog.Warn("Could not create sandbox cache directory", "dir", dir, "error", err)
		return ""
	}
	return dir
}

// EnsureSandboxAvailable implements the fail-if-unavailable contract: when the
// user requires a sandbox, startup fails loudly with the same actionable
// message the runtime refusal uses.
func (s *ConfigStore) EnsureSandboxAvailable(rt SandboxRuntime) error {
	if !rt.Configuration.FailIfUnavailable || rt.Policy == nil {
		if rt.Err != nil && rt.Configuration.FailIfUnavailable {
			return fmt.Errorf("sandbox required but could not be configured: %w", rt.Err)
		}
		return nil
	}
	if rt.Policy.Mode == sandbox.ModeDangerFullAccess {
		return fmt.Errorf("sandbox-fail-if-unavailable is set but the sandbox mode is danger-full-access")
	}
	status := sandbox.Describe(context.Background(), rt.Policy)
	if status.Backend == "none" {
		return fmt.Errorf("sandbox required but unavailable: %s", status.Missing)
	}
	return nil
}

// SandboxStatus describes the current policy and enforcement for display.
func (s *ConfigStore) SandboxStatus(rt SandboxRuntime) sandbox.Status {
	return sandbox.Describe(context.Background(), rt.Policy)
}

// ValidateSandboxModeFlag validates a mode supplied on the command line.
func ValidateSandboxModeFlag(value string) (sandbox.Mode, error) {
	if value == "" {
		return "", nil
	}
	mode, err := sandbox.ValidateMode(value)
	if err != nil {
		return "", err
	}
	return mode, nil
}
