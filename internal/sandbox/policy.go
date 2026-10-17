// Package sandbox defines which filesystem paths a tool execution may write,
// and the boundary that enforces it.
//
// The package owns the single semantic definition of "writable": a mode plus
// a list of canonical roots, minus protected paths. The shell package, the
// mutating file tools, and every OS backend consume it. No backend may
// hard-code a root, /tmp included.
package sandbox

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"slices"
	"strings"
)

// Mode is the confinement level of a session or a single tool call.
type Mode string

const (
	// ModeReadOnly allows no writes except required sinks (/dev/null) and a
	// private per-run temp directory.
	ModeReadOnly Mode = "read-only"
	// ModeWorkspaceWrite allows writes under the configured writable roots.
	ModeWorkspaceWrite Mode = "workspace-write"
	// ModeDangerFullAccess disables confinement entirely. It is only
	// reachable by explicit selection.
	ModeDangerFullAccess Mode = "danger-full-access"
)

// Modes returns every selectable mode, widest last.
func Modes() []Mode {
	return []Mode{ModeReadOnly, ModeWorkspaceWrite, ModeDangerFullAccess}
}

// ValidateMode reports whether s names a known mode.
func ValidateMode(s string) (Mode, error) {
	m := Mode(s)
	if slices.Contains(Modes(), m) {
		return m, nil
	}
	return "", fmt.Errorf("unknown sandbox mode %q (accepted: read-only, workspace-write, danger-full-access)", s)
}

// wider reports whether m is strictly wider than other.
func (m Mode) wider(other Mode) bool {
	return rank(m) > rank(other)
}

func rank(m Mode) int {
	switch m {
	case ModeReadOnly:
		return 0
	case ModeWorkspaceWrite:
		return 1
	case ModeDangerFullAccess:
		return 2
	default:
		return -1
	}
}

// Enforcement is the strength of the boundary a backend actually established.
type Enforcement string

const (
	// EnforcementFull means the backend enforces every guarantee the policy
	// asks for.
	EnforcementFull Enforcement = "full"
	// EnforcementPartial means the backend enforces a subset: the status and
	// logs must say which guarantee is missing.
	EnforcementPartial Enforcement = "partial"
	// EnforcementNone means no boundary was established.
	EnforcementNone Enforcement = "none"
)

// Policy is the resolved confinement for one execution.
//
// Paths are canonical (absolute, symlink-resolved, cleaned) and WriteRoots is
// deduplicated. A nil *Policy means "no policy attached": callers that have
// not opted in keep their existing behavior, which is what keeps the shell
// package usable outside a session (hooks, config loading, tests).
type Policy struct {
	Mode      Mode
	Workspace string
	// WriteRoots includes Workspace. Order is stable so generated backend
	// rules are deterministic.
	WriteRoots []string
	// DenyWrites are paths inside WriteRoots that stay unwritable.
	DenyWrites []string
	// Protected are paths that grant future code execution and are always
	// writable-denied, even inside a root. They cannot be re-allowed.
	Protected []string
	// TempDir is private to this run and written into the environment.
	TempDir string
	// CacheDir is the user cache directory, included by default so build
	// tools work out of the box.
	CacheDir string
}

// Denial is a policy refusal. It is the single vocabulary shared by the
// in-process fence, the file tools, and every backend: a kernel denial and a
// file-tool denial must look the same to the model.
type Denial struct {
	// Tool is the tool that was refused, when known.
	Tool string
	// Path is the refused path.
	Path string
	// Mode is the mode that applied.
	Mode Mode
	// Protected reports whether the path was refused because it is protected
	// rather than merely outside the writable set.
	Protected bool
	// Remedy names the setting a user can change to allow the write.
	Remedy string
	// EscalationHint is set when the caller may request a wider mode.
	EscalationHint bool
}

// Marker is the stable, model-recognizable prefix of a denial. The same text
// is used by every confined tool.
const Marker = "sandbox-denied"

func (d Denial) Error() string {
	var b strings.Builder
	fmt.Fprintf(&b, "%s mode=%s path=%s", Marker, d.Mode, d.Path)
	if d.Protected {
		b.WriteString(" reason=protected")
	} else {
		b.WriteString(" reason=outside-writable-roots")
	}
	if d.Remedy != "" {
		fmt.Fprintf(&b, " remedy=%s", d.Remedy)
	}
	if d.EscalationHint {
		b.WriteString(" hint=sandbox escalation is available: retry with sandbox_permissions and a justification to request this mode for one call")
	}
	return b.String()
}

// IsDenial reports whether err is a policy denial.
func IsDenial(err error) bool {
	if IsRunnerError(err) {
		return false
	}
	var d Denial
	return errors.As(err, &d)
}

// AsDenial returns the denial carried by err, if any. A denial wrapped inside
// a runner failure is not reported: the command never ran.
func AsDenial(err error) (Denial, bool) {
	if IsRunnerError(err) {
		return Denial{}, false
	}
	var d Denial
	if errors.As(err, &d) {
		return d, true
	}
	return Denial{}, false
}

// Config is the un-resolved user intent: what the configuration files asked
// for. It is deliberately separate from Policy so the same settings can be
// layered (project files may tighten but never widen).
type Config struct {
	// Mode is the requested mode. Empty means "use the default".
	Mode Mode
	// AllowWrite are extra writable roots.
	AllowWrite []string
	// DenyWrite are paths to keep unwritable inside the writable roots.
	DenyWrite []string
	// ExcludeCommands are command patterns that run outside the sandbox.
	ExcludeCommands []string
	// FailIfUnavailable refuses to start when the sandbox cannot be
	// established.
	FailIfUnavailable bool
}

// DefaultMode is the mode a session uses when nothing is configured.
const DefaultMode = ModeWorkspaceWrite

// Options are the inputs needed to resolve a policy.
type Options struct {
	// Workspace is the session working directory.
	Workspace string
	// TempDir is the private per-run temp directory. It is created if
	// missing.
	TempDir string
	// CacheDir is the user cache directory.
	CacheDir string
	// Config is the user's requested settings.
	Config Config
	// Protected are extra protected paths to include.
	Protected []string
	// GlobalConfigDir and GlobalDataDir are Crush's configuration and data
	// directories, protected so the agent cannot grant itself permissions.
	GlobalConfigDir string
	GlobalDataDir   string
}

// newTempDir creates the private per-run temp directory. A failure is
// tolerated: the temp root is then simply absent from the writable set, which
// is safer than failing the whole session.
func ensureDir(path string) string {
	if path == "" {
		return ""
	}
	_ = os.MkdirAll(path, 0o700)
	return CanonicalPath(path)
}

// Resolve turns user intent into a Policy. This is the only place that decides
// what "writable" means.
func Resolve(opts Options) (*Policy, error) {
	mode := opts.Config.Mode
	if mode == "" {
		mode = DefaultMode
	}
	if _, err := ValidateMode(string(mode)); err != nil {
		return nil, err
	}

	workspace, err := canonicalExisting(opts.Workspace)
	if err != nil {
		return nil, fmt.Errorf("resolving sandbox workspace: %w", err)
	}

	p := &Policy{
		Mode:      mode,
		Workspace: workspace,
		TempDir:   ensureDir(opts.TempDir),
		CacheDir:  canonicalOptional(opts.CacheDir),
	}

	switch mode {
	case ModeReadOnly:
		if p.TempDir != "" {
			p.WriteRoots = []string{p.TempDir}
		}
	case ModeWorkspaceWrite:
		roots := []string{workspace}
		if p.TempDir != "" {
			roots = append(roots, p.TempDir)
		}
		if p.CacheDir != "" {
			roots = append(roots, p.CacheDir)
		}
		for _, root := range opts.Config.AllowWrite {
			canonical, err := canonicalAllowRoot(root)
			if err != nil {
				return nil, err
			}
			roots = append(roots, canonical)
		}
		p.WriteRoots = dedupe(roots)
	case ModeDangerFullAccess:
		// Unbounded: no roots are consulted.
	}

	for _, deny := range opts.Config.DenyWrite {
		canonical := canonicalOptional(deny)
		if canonical == "" {
			continue
		}
		if !slices.Contains(p.DenyWrites, canonical) {
			p.DenyWrites = append(p.DenyWrites, canonical)
		}
	}

	protected := DefaultProtectedPaths(workspace, opts.GlobalConfigDir, opts.GlobalDataDir)
	protected = append(protected, opts.Protected...)
	p.Protected = CanonicalProtectedPaths(protected)
	return p, nil
}

// UniquePaths deduplicates paths, preserving order. It is exported so a
// caller building a policy (an escalation, for example) derives the writable
// set the same way resolution does.
func UniquePaths(paths []string) []string { return dedupe(paths) }

func dedupe(paths []string) []string {
	seen := make(map[string]bool, len(paths))
	out := make([]string, 0, len(paths))
	for _, p := range paths {
		if p == "" || seen[p] {
			continue
		}
		seen[p] = true
		out = append(out, p)
	}
	return out
}

func canonicalExisting(path string) (string, error) {
	if path == "" {
		return "", errors.New("path is required")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved, nil
	}
	if _, err := os.Stat(abs); err != nil {
		return "", err
	}
	return filepath.Clean(abs), nil
}

func canonicalOptional(path string) string {
	if path == "" {
		return ""
	}
	return CanonicalPath(path)
}

// canonicalAllowRoot resolves an extra writable root, reporting a root that
// does not exist rather than silently ignoring it.
func canonicalAllowRoot(path string) (string, error) {
	abs, err := filepath.Abs(homeExpanded(path))
	if err != nil {
		return "", fmt.Errorf("sandbox allow-write %q: %w", path, err)
	}
	resolved, err := filepath.EvalSymlinks(abs)
	if err != nil {
		if os.IsNotExist(err) {
			return "", fmt.Errorf("sandbox allow-write %q: directory does not exist", path)
		}
		return "", fmt.Errorf("sandbox allow-write %q: %w", path, err)
	}
	return resolved, nil
}

// CanonicalPath returns the absolute, symlink-resolved, cleaned form of path.
// A path that does not exist resolves as far as its nearest existing ancestor
// so a to-be-created file is compared in the same space as an existing one.
func CanonicalPath(path string) string {
	if path == "" {
		return ""
	}
	path = homeExpanded(path)
	abs, err := filepath.Abs(path)
	if err != nil {
		return filepath.Clean(path)
	}
	if resolved, err := filepath.EvalSymlinks(abs); err == nil {
		return resolved
	}
	// Resolve the deepest existing ancestor, then re-join the remainder.
	remainder := ""
	current := abs
	for {
		parent := filepath.Dir(current)
		if parent == current {
			break
		}
		remainder = filepath.Join(filepath.Base(current), remainder)
		current = parent
		if resolved, err := filepath.EvalSymlinks(current); err == nil {
			return filepath.Join(resolved, remainder)
		}
	}
	return filepath.Clean(abs)
}

func homeExpanded(path string) string {
	if path == "~" || strings.HasPrefix(path, "~/") {
		home, err := os.UserHomeDir()
		if err == nil {
			return filepath.Join(home, strings.TrimPrefix(path, "~"))
		}
	}
	return path
}

// within reports whether path is root or lives underneath it.
func within(path, root string) bool {
	if selfOrAncestor(path, root) {
		return true
	}
	rel, err := filepath.Rel(root, path)
	if err != nil {
		return false
	}
	return rel != ".." && !strings.HasPrefix(rel, ".."+string(filepath.Separator))
}

// selfOrAncestor reports whether the two paths are different spellings of the
// same canonical directory.
func selfOrAncestor(a, b string) bool {
	return a == b
}

// CheckWrite reports whether path may be written under the policy. It resolves
// symlinks first, so a link inside a root pointing outside it is refused.
func (p *Policy) CheckWrite(path string) error {
	if p == nil || p.Mode == ModeDangerFullAccess {
		return nil
	}
	canonical := CanonicalPath(path)

	if isSink(canonical) {
		return nil
	}

	if p.isProtected(canonical) {
		return Denial{
			Path:      canonical,
			Mode:      p.Mode,
			Protected: true,
			Remedy:    "this path grants future code execution and cannot be made writable",
		}
	}

	if p.isDenied(canonical) {
		return Denial{
			Path:   canonical,
			Mode:   p.Mode,
			Remedy: "remove the matching sandbox deny-write entry",
		}
	}

	for _, root := range p.WriteRoots {
		if within(canonical, root) {
			return nil
		}
	}

	return Denial{
		Path:   canonical,
		Mode:   p.Mode,
		Remedy: "add the directory to sandbox allow-write (or run with sandbox mode danger-full-access)",
	}
}

// isDenied reports whether path is a configured write denial or inside one.
func (p *Policy) isDenied(path string) bool {
	for _, deny := range p.DenyWrites {
		if within(path, deny) {
			return true
		}
	}
	return false
}

// isProtected reports whether path is protected or inside a protected path.
func (p *Policy) isProtected(path string) bool {
	for _, prot := range p.Protected {
		if within(path, prot) {
			return true
		}
	}
	return false
}

// isSink reports whether path is a required write sink that every mode must
// allow.
func isSink(path string) bool {
	switch filepath.ToSlash(path) {
	case "/dev/null", "/dev/stdout", "/dev/stderr", "/dev/zero", "/dev/tty":
		return true
	}
	return false
}

// WriteRootsFor returns the roots a backend should mount writable.
func (p *Policy) WriteRootsFor() []string {
	if p == nil {
		return nil
	}
	return slices.Clone(p.WriteRoots)
}

// DescendantProtected returns the protected paths that sit inside a writable
// root, i.e. the ones a backend must subtract. A backend that cannot subtract
// reports partial enforcement when this is non-empty.
func (p *Policy) ProtectedInsideRoots() []string {
	if p == nil {
		return nil
	}
	var inside []string
	for _, prot := range p.Protected {
		for _, root := range p.WriteRoots {
			if within(prot, root) {
				inside = append(inside, prot)
				break
			}
		}
	}
	return inside
}

// RequiresSubtraction reports whether the policy needs protected paths to be
// subtracted from a writable root.
func (p *Policy) RequiresSubtraction() bool {
	return len(p.ProtectedInsideRoots()) > 0
}

// String renders the resolved policy for diagnostics.
func (p *Policy) String() string {
	if p == nil {
		return "mode=<none>"
	}
	var b strings.Builder
	fmt.Fprintf(&b, "mode=%s workspace=%s", p.Mode, p.Workspace)
	if len(p.WriteRoots) > 0 {
		fmt.Fprintf(&b, " write_roots=%s", strings.Join(p.WriteRoots, ":"))
	}
	if len(p.DenyWrites) > 0 {
		fmt.Fprintf(&b, " deny_writes=%s", strings.Join(p.DenyWrites, ":"))
	}
	if len(p.Protected) > 0 {
		fmt.Fprintf(&b, " protected=%s", strings.Join(p.Protected, ":"))
	}
	return b.String()
}

// IsWiderMode reports whether candidate is strictly wider than base. It is
// used to validate an escalation request and to check that project-level
// configuration does not widen the sandbox.
func IsWiderMode(candidate, base Mode) bool {
	return candidate.wider(base)
}
