package config

import (
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
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
			Mode:       mode,
			AllowWrite: resolveAgainstWorkspace(cfg.AllowWrite, opts.Workspace),
			DenyWrite:  resolveAgainstWorkspace(cfg.DenyWrite, opts.Workspace),
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
	if project.Mode != "" {
		effective := base.Mode
		if effective == "" {
			effective = string(sandbox.DefaultMode)
		}
		if widensMode(sandbox.Mode(project.Mode), sandbox.Mode(effective)) {
			return fmt.Errorf("%w: permissions.sandbox.mode %q would widen %q", ErrProjectWidensSandbox, project.Mode, effective)
		}
	}
	if len(project.AllowWrite) > 0 {
		return fmt.Errorf("%w: permissions.sandbox.allow_write may only be set in user configuration", ErrProjectWidensSandbox)
	}
	return nil
}

func widensMode(candidate, base sandbox.Mode) bool {
	return sandbox.IsWiderMode(candidate, base)
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

// sandboxTempPathLimit is the longest path the temp directory may produce
// before tools that bind Unix sockets inside it fail with EINVAL. Linux's
// sun_path is 108 bytes including the terminator, and a socket name costs at
// least a few more, so leave generous headroom.
const sandboxTempPathLimit = 60

// DefaultSandboxTempDir returns the private per-run temp directory for a data
// directory.
//
// It normally lives under the data directory, so it is removed with the rest
// of the project's state and never shares a namespace with /tmp. A deeply
// nested data directory (the default per-project layout under
// $XDG_DATA_HOME already exceeds 60 bytes) is replaced by a short path under
// the system temp directory instead: TMPDIR is exported to every confined
// command, and a too-long value makes every Unix-socket bind inside it fail.
// The directory is still private to the project (0700) and carry a stable
// suffix so it is reused across runs rather than accumulating.
func DefaultSandboxTempDir(dataDir string) string {
	if dataDir == "" {
		return ""
	}
	candidate := filepath.Join(dataDir, "sandbox", "tmp")
	if len(candidate) <= sandboxTempPathLimit {
		return candidate
	}
	// The system temp directory is not necessarily short (it is TMPDIR, which
	// may itself be a deep path), so prefer whichever known base produces a
	// path that fits. /tmp is checked last because it is the conventional
	// short location.
	name := "crush-" + shortPathHash(dataDir)
	for _, base := range tempDirCandidates() {
		path := filepath.Join(base, name)
		if len(path) <= sandboxTempPathLimit {
			return path
		}
	}
	return filepath.Join(tempDirCandidates()[0], name)
}

// tempDirCandidates lists the locations a too-deep project data directory may
// fall back to, shortest first.
func tempDirCandidates() []string {
	candidates := []string{os.TempDir()}
	if runtime.GOOS != "windows" {
		candidates = append(candidates, "/tmp")
	}
	return candidates
}

// shortPathHash returns a short, stable digest of a path, used to keep
// per-project state distinct when the project data directory is too deep to
// live under.
func shortPathHash(path string) string {
	sum := sha256.Sum256([]byte(filepath.Clean(path)))
	return hex.EncodeToString(sum[:6])
}

// mergeSandboxTighten merges user-level and project-level sandbox settings.
//
// A project may only tighten, so it contributes two things: additional write
// denials, and a narrower mode (checked by [CheckProjectSandboxTightens]).
// Everything else — in particular write roots — comes from the user-level
// settings. Dropping a narrower project mode would silently ignore the safer
// of the two configurations.
func mergeSandboxTighten(base, project Sandbox) Sandbox {
	merged := base
	if project.Mode != "" {
		effective := base.Mode
		if effective == "" {
			effective = string(sandbox.DefaultMode)
		}
		if !widensMode(sandbox.Mode(project.Mode), sandbox.Mode(effective)) {
			merged.Mode = project.Mode
		}
	}
	for _, deny := range project.DenyWrite {
		if !slices.Contains(merged.DenyWrite, deny) {
			merged.DenyWrite = append(merged.DenyWrite, deny)
		}
	}
	return merged
}
