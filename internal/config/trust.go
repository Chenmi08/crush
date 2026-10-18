package config

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
	"sync"
)

// trustFileName is the file under the global data directory that records
// which projects the user has trusted to execute repository-local
// configuration.
const trustFileName = "trusted-projects.json"

// trustList is the on-disk shape of the trust file. It is a list rather than
// a map so the file stays readable and diffable; ordering is not significant.
type trustList struct {
	// Projects holds canonical project paths (see CanonicalProjectPath).
	Projects []string `json:"projects"`
}

var trustMu sync.Mutex

// projectTrustPath returns the path to the trust file. It lives beside the
// projects registry in the global data directory so it survives across
// sessions and checkouts.
func projectTrustPath() string {
	return filepath.Join(filepath.Dir(GlobalConfigData()), trustFileName)
}

// CanonicalProjectPath returns the canonical form of a project path used as
// the trust key: absolute, symlink-resolved, and cleaned. Two checkouts of the
// same repository resolve to different paths and are therefore separate
// decisions; a symlink to a trusted checkout resolves to the same path and
// inherits its trust.
//
// The path does not need to exist: a missing path falls back to the cleaned
// absolute form, which still distinguishes distinct directories.
func CanonicalProjectPath(path string) string {
	abs, err := filepath.Abs(path)
	if err != nil {
		abs = filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	return filepath.Clean(abs)
}

// IsProjectTrusted reports whether the project at path has been trusted to
// execute its repository-local configuration. The check is keyed by the
// canonical project path.
func IsProjectTrusted(path string) bool {
	trusted, err := LoadProjectTrust()
	if err != nil {
		// A broken trust store must not grant trust.
		return false
	}
	return slices.Contains(trusted, CanonicalProjectPath(path))
}

// LoadProjectTrust returns the canonical paths of every trusted project.
func LoadProjectTrust() ([]string, error) {
	trustMu.Lock()
	defer trustMu.Unlock()
	return loadProjectTrustLocked()
}

func loadProjectTrustLocked() ([]string, error) {
	data, err := os.ReadFile(projectTrustPath())
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var list trustList
	if err := json.Unmarshal(data, &list); err != nil {
		return nil, fmt.Errorf("parsing %s: %w", projectTrustPath(), err)
	}
	return list.Projects, nil
}

// TrustProject records the project at path as trusted. It is idempotent.
func TrustProject(path string) error {
	canonical := CanonicalProjectPath(path)
	trustMu.Lock()
	defer trustMu.Unlock()

	trusted, err := loadProjectTrustLocked()
	if err != nil {
		return err
	}
	if slices.Contains(trusted, canonical) {
		return nil
	}
	return writeProjectTrustLocked(append(trusted, canonical))
}

// RevokeProjectTrust withdraws trust from the project at path. Revoking a
// project that was never trusted is a no-op.
func RevokeProjectTrust(path string) error {
	canonical := CanonicalProjectPath(path)
	trustMu.Lock()
	defer trustMu.Unlock()

	trusted, err := loadProjectTrustLocked()
	if err != nil {
		return err
	}
	kept := slices.DeleteFunc(trusted, func(p string) bool { return p == canonical })
	if len(kept) == len(trusted) {
		return nil
	}
	return writeProjectTrustLocked(kept)
}

func writeProjectTrustLocked(trusted []string) error {
	slices.Sort(trusted)
	data, err := json.MarshalIndent(trustList{Projects: trusted}, "", "  ")
	if err != nil {
		return err
	}
	path := projectTrustPath()
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return err
	}
	return atomicWriteFile(path, append(data, '\n'), 0o600)
}

// ProjectConfigCandidates returns the repository-local configuration files
// that would execute for cwd: every discovered crushrc and crush.json inside
// the project boundary. Global user-level configuration is excluded, because
// it is not repository-local and never requires trust.
//
// This is the list shown to the user when a project is first encountered and
// the list whose execution the trust gate controls.
func ProjectConfigCandidates(cwd string) []string {
	var local []string
	for _, path := range lookupConfigs(cwd) {
		if path == "" || isGlobalConfigPath(path) {
			continue
		}
		local = append(local, path)
	}
	return local
}

// isGlobalConfigPath reports whether path is one of the user-level or
// machine-owned config files rather than a repository-local one.
func isGlobalConfigPath(path string) bool {
	clean := filepath.Clean(path)
	for _, global := range []string{
		systemConfigPath,
		GlobalConfig(),
		shellConfigSibling(GlobalConfig()),
		GlobalConfigData(),
	} {
		if global == "" {
			continue
		}
		if clean == filepath.Clean(global) {
			return true
		}
	}
	return false
}

// shouldExecuteProjectConfig reports whether repository-local configuration
// may run for the given working directory, and returns the local files that
// would execute. It never prompts: callers that have a terminal ask the user
// (see [TrustPrompt]) and record the answer before loading.
//
// The conservative default is to refuse: an untrusted project's configuration
// is not executed, and only user-level configuration applies.
func shouldExecuteProjectConfig(cwd string) bool {
	local := ProjectConfigCandidates(cwd)
	if len(local) == 0 {
		return true
	}
	return IsProjectTrusted(projectTrustKey(cwd))
}

// ProjectTrustKey is the path trust is keyed by: the project boundary (the
// git working tree root when detected), canonicalized. Keying by the boundary
// rather than the working directory means trust follows the repository no
// matter which subdirectory Crush was started in, while two checkouts of the
// same repository remain separate decisions.
//
// Every writer and reader of the trust store must use this function. The CLI
// used to record the plain working directory instead, so trusting a project
// from a subdirectory had no effect and revoking it revoked nothing.
func ProjectTrustKey(cwd string) string {
	return CanonicalProjectPath(projectBoundary(cwd))
}

// projectTrustKey is the internal spelling of [ProjectTrustKey].
func projectTrustKey(cwd string) string {
	return ProjectTrustKey(cwd)
}

// TrustPrompt describes the one-time decision required before a project's
// repository-local configuration is executed.
type TrustPrompt struct {
	// Project is the canonical project path the decision is recorded against.
	Project string
	// Files are the repository-local config files that would run.
	Files []string
}

// NewTrustPrompt builds the trust prompt for cwd, or returns false when the
// project has no repository-local configuration and therefore needs no
// decision.
func NewTrustPrompt(cwd string) (TrustPrompt, bool) {
	files := ProjectConfigCandidates(cwd)
	if len(files) == 0 {
		return TrustPrompt{}, false
	}
	return TrustPrompt{
		Project: projectTrustKey(cwd),
		Files:   files,
	}, true
}

// Message renders the prompt shown to the user.
func (p TrustPrompt) Message() string {
	var b strings.Builder
	b.WriteString("This project ships configuration that Crush would execute as shell commands:\n")
	for _, f := range p.Files {
		b.WriteString("  " + f + "\n")
	}
	b.WriteString("Trust this project? Only trust repositories you would run yourself.")
	return b.String()
}
