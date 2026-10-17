package config

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/sandbox"
)

// SandboxConfig returns the sandbox settings, or zero values when unset.
func (c *Permissions) SandboxConfig() Sandbox {
	if c == nil || c.Sandbox == nil {
		return Sandbox{}
	}
	return *c.Sandbox
}

// ResolveSandbox turns the configuration into a resolved policy.
//
// It is the single place that reads the sandbox settings, so project-level
// layering cannot bypass validation. A project-level configuration may tighten
// the sandbox (denials, exclusions) but never widen it: widening keys are
// rejected before a policy is built.
//
// opts carries the directories the policy needs: TempDir, CacheDir, and the
// global config/data directories to protect.
type SandboxResolveOptions struct {
	Workspace       string
	TempDir         string
	CacheDir        string
	GlobalConfigDir string
	GlobalDataDir   string
	// ProjectScoped reports that the settings came from repository-local
	// configuration, which may only tighten.
	ProjectScoped bool
	// Base is the wider, user-level request a project-scoped configuration
	// is applied on top of.
	Base *Sandbox
}

// ErrProjectWidensSandbox reports a project-level attempt to widen the
// sandbox.
var ErrProjectWidensSandbox = fmt.Errorf("project-level configuration may not widen the sandbox")

// ResolveSandbox builds a policy from the configured sandbox settings.
func ResolveSandbox(cfg Sandbox, opts SandboxResolveOptions) (*sandbox.Policy, error) {
	mode := sandbox.Mode(cfg.Mode)
	if cfg.Mode == "" {
		mode = sandbox.DefaultMode
	} else {
		validated, err := sandbox.ValidateMode(cfg.Mode)
		if err != nil {
			return nil, fmt.Errorf("permissions.sandbox.mode: %w", err)
		}
		mode = validated
	}

	if opts.ProjectScoped {
		base := Sandbox{}
		if opts.Base != nil {
			base = *opts.Base
		}
		if err := CheckProjectSandboxTightens(cfg, base); err != nil {
			return nil, err
		}
	}

	return sandbox.Resolve(sandbox.Options{
		Workspace:       opts.Workspace,
		TempDir:         opts.TempDir,
		CacheDir:        opts.CacheDir,
		GlobalConfigDir: opts.GlobalConfigDir,
		GlobalDataDir:   opts.GlobalDataDir,
		Config: sandbox.Config{
			Mode:              mode,
			AllowWrite:        resolveAgainstWorkspace(cfg.AllowWrite, opts.Workspace),
			DenyWrite:         resolveAgainstWorkspace(cfg.DenyWrite, opts.Workspace),
			ExcludeCommands:   cfg.ExcludeCommands,
			FailIfUnavailable: cfg.FailIfUnavailable,
		},
	})
}

// resolveAgainstWorkspace resolves relative sandbox paths against the
// workspace rather than the process working directory, so a checked-out
// repository's `./secrets` means the repository's own secrets directory.
func resolveAgainstWorkspace(paths []string, workspace string) []string {
	if len(paths) == 0 {
		return nil
	}
	resolved := make([]string, 0, len(paths))
	for _, path := range paths {
		path = strings.TrimSpace(path)
		if path == "" {
			continue
		}
		if !filepath.IsAbs(path) && !strings.HasPrefix(path, "~") {
			path = filepath.Join(workspace, path)
		}
		resolved = append(resolved, path)
	}
	return resolved
}

// CheckProjectSandboxTightens rejects a project-level configuration that
// widens the sandbox relative to base. It reports the offending key.
func CheckProjectSandboxTightens(project, base Sandbox) error {
	if project.Mode != "" && project.Mode != base.Mode {
		if base.Mode == "" || widensMode(sandbox.Mode(project.Mode), sandbox.Mode(base.Mode)) {
			return fmt.Errorf("%w: permissions.sandbox.mode %q would widen %q", ErrProjectWidensSandbox, project.Mode, base.Mode)
		}
	}
	if len(project.AllowWrite) > 0 {
		return fmt.Errorf("%w: permissions.sandbox.allow_write may only be set in user configuration", ErrProjectWidensSandbox)
	}
	if project.FailIfUnavailable && !base.FailIfUnavailable {
		return fmt.Errorf("%w: permissions.sandbox.fail_if_unavailable may only be set in user configuration", ErrProjectWidensSandbox)
	}
	return nil
}

func widensMode(candidate, base sandbox.Mode) bool {
	order := map[sandbox.Mode]int{
		sandbox.ModeReadOnly:         0,
		sandbox.ModeWorkspaceWrite:   1,
		sandbox.ModeDangerFullAccess: 2,
	}
	return order[candidate] > order[base]
}

// ValidateSandbox validates the sandbox settings and reports the offending
// key and the accepted values for every error.
func ValidateSandbox(cfg Sandbox) error {
	if cfg.Mode != "" {
		if _, err := sandbox.ValidateMode(cfg.Mode); err != nil {
			return fmt.Errorf("permissions.sandbox.mode: %w", err)
		}
	}
	for _, root := range cfg.AllowWrite {
		if strings.TrimSpace(root) == "" {
			return fmt.Errorf("permissions.sandbox.allow_write: empty path; remove the entry or name a directory")
		}
	}
	for _, deny := range cfg.DenyWrite {
		if strings.TrimSpace(deny) == "" {
			return fmt.Errorf("permissions.sandbox.deny_write: empty path; remove the entry or name a path")
		}
	}
	return nil
}

// IsSandboxExcluded reports whether a command matches a configured exclusion.
// Patterns are matched as a case-sensitive glob-style prefix: "docker *"
// matches any command starting with "docker ". A pattern with no * is an exact
// command-name match.
func IsSandboxExcluded(command string, patterns []string) bool {
	command = strings.TrimSpace(command)
	for _, pattern := range patterns {
		if matchExclusion(command, strings.TrimSpace(pattern)) {
			return true
		}
	}
	return false
}

func matchExclusion(command, pattern string) bool {
	if pattern == "" {
		return false
	}
	if !strings.Contains(pattern, "*") {
		fields := strings.Fields(command)
		if len(fields) == 0 {
			return false
		}
		return fields[0] == pattern || command == pattern
	}
	prefix, suffix, _ := strings.Cut(pattern, "*")
	if suffix == "" {
		return strings.HasPrefix(command, prefix)
	}
	if prefix == "" {
		return strings.HasSuffix(command, suffix)
	}
	return strings.HasPrefix(command, prefix) && strings.HasSuffix(command, suffix)
}

// DefaultSandboxTempDir returns the private per-run temp directory for a data
// directory. Keeping it under the data directory means it is removed with the
// rest of the run's state and never shares a namespace with /tmp.
func DefaultSandboxTempDir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	return filepath.Join(dataDir, "sandbox", "tmp")
}

// mergeSandboxTighten merges user-level and project-level sandbox settings.
// Project settings may only add denials and exclusions; everything else comes
// from the user-level settings.
func mergeSandboxTighten(base, project Sandbox) Sandbox {
	merged := base
	for _, deny := range project.DenyWrite {
		if !slices.Contains(merged.DenyWrite, deny) {
			merged.DenyWrite = append(merged.DenyWrite, deny)
		}
	}
	for _, ex := range project.ExcludeCommands {
		if !slices.Contains(merged.ExcludeCommands, ex) {
			merged.ExcludeCommands = append(merged.ExcludeCommands, ex)
		}
	}
	return merged
}
