//go:build linux

package sandbox

import (
	"os"
	"path/filepath"
	"slices"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

// goldenPolicy builds a real policy rooted at a temp directory, so the
// generated runner arguments exercise every bind path rather than skipping
// roots that do not exist. The temp root is normalized out of the golden text
// so the file is stable across runs.
func goldenPolicy(t *testing.T) (*Policy, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	cache := filepath.Join(root, "cache")
	extra := filepath.Join(root, "extra")
	for _, dir := range []string{
		workspace,
		cache,
		extra,
		filepath.Join(workspace, ".git", "hooks"),
		filepath.Join(workspace, "secrets"),
	} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}
	// One protected file that exists (so it is bound read-only) and several
	// that do not (so the placeholder bind form is characterized).
	require.NoError(t, os.WriteFile(filepath.Join(workspace, "crushrc"), nil, 0o644))

	return &Policy{
		Mode:       ModeWorkspaceWrite,
		Workspace:  workspace,
		WriteRoots: []string{workspace, extra, cache},
		TempDir:    filepath.Join(root, "tmp"),
		CacheDir:   cache,
		DenyWrites: []string{CanonicalPath(filepath.Join(workspace, "secrets"))},
		Protected: CanonicalProtectedPaths([]string{
			filepath.Join(workspace, "crushrc"),
			filepath.Join(workspace, ".git", "hooks"),
			filepath.Join(workspace, ".git", "config"),
		}),
	}, root
}

// sandboxPlaceholder is the placeholder file the backend binds over a
// protected path that does not exist yet.
func sandboxPlaceholder(p *Policy) string {
	path, _ := p.protectedPlaceholder()
	return path
}

// TestBubblewrapArgs_Golden pins the generated runner arguments. The
// generated text is the artifact: a refactor must not widen the sandbox
// silently.
func TestBubblewrapArgs_Golden(t *testing.T) {
	policy, root := goldenPolicy(t)
	args, err := BubblewrapArgs(policy, "/bin/sh", []string{"-c", "echo hi"})
	require.NoError(t, err)

	var b strings.Builder
	for _, arg := range args {
		b.WriteString(normalizeRoots(arg, root))
		b.WriteString("\n")
	}
	golden.RequireEqual(t, []byte(b.String()))
}

// TestBubblewrapArgs_NoHardcodedRoots pins the rule the parity tests enforce:
// no root is spelled literally in the backend. Every absolute path in the
// generated args must come from the policy or be a runner-mandated
// pseudo-filesystem.
func TestBubblewrapArgs_NoHardcodedRoots(t *testing.T) {
	policy, root := goldenPolicy(t)
	args, err := BubblewrapArgs(policy, "/bin/sh", nil)
	require.NoError(t, err)

	allowed := map[string]bool{
		"/": true, "/dev": true, "/proc": true,
		"/dev/null": true, "/bin/sh": true,
		sandboxPlaceholder(policy): true,
	}
	for _, writeRoot := range policy.WriteRoots {
		allowed[writeRoot] = true
	}
	for _, prot := range policy.Protected {
		allowed[prot] = true
	}
	for _, deny := range policy.DenyWrites {
		allowed[deny] = true
	}

	// The temp root is normalized away: it is a policy path, not a literal
	// the backend chose.
	for _, arg := range args {
		if !strings.HasPrefix(arg, "/") {
			continue
		}
		arg = normalizeRoots(arg, root)
		if arg == "<ROOT>/tmp" {
			arg = normalizeRoots(policy.TempDir, root)
		}
		require.True(t, allowed[arg] || strings.HasPrefix(arg, "<ROOT>"),
			"backend spelled an unexpected root literally: %s", arg)
	}

	// The backend may only name paths the policy carries, plus the runner's
	// own pseudo-filesystems. "/tmp" is checked after normalization so the
	// test's own temp directory does not trip it.
	normalized := normalizeRoots(strings.Join(args, " "), root)
	require.NotContains(t, normalized, " /tmp ",
		"the backend must not hard-code /tmp")
	require.Contains(t, joinedRaw(args), root, "the policy's own roots must be present")
}

// joinedRaw joins the args, for assertions that inspect absolute paths.
func joinedRaw(args []string) string { return strings.Join(args, " ") }

// TestBubblewrapArgs_ParityWithSharedPolicy states the cross-backend parity
// requirement in executable form: the writable set described by the generated
// rules equals the shared definition's writable set.
func TestBubblewrapArgs_ParityWithSharedPolicy(t *testing.T) {
	policy, _ := goldenPolicy(t)
	args, err := BubblewrapArgs(policy, "/bin/sh", nil)
	require.NoError(t, err)

	var bound []string
	for i, arg := range args {
		if arg != "--bind" || i+2 >= len(args) {
			continue
		}
		bound = append(bound, args[i+1])
		require.Equal(t, args[i+1], args[i+2], "bind src and dest must match")
	}

	var want []string
	for _, writeRoot := range policy.WriteRootsFor() {
		if _, err := os.Stat(writeRoot); err == nil {
			want = append(want, writeRoot)
		}
	}
	require.Equal(t, want, bound)
}

// TestBubblewrapArgs_WorkingDir verifies the runner starts in the command's
// own directory. The shell sets cmd.Dir to the interpreter's cwd, so a
// hardcoded workspace --chdir would silently ignore `cd sub && ...` and
// working_dir, which Landlock and Seatbelt honour.
func TestBubblewrapArgs_WorkingDir(t *testing.T) {
	policy, _ := goldenPolicy(t)
	sub := filepath.Join(policy.Workspace, "sub")
	require.NoError(t, os.MkdirAll(sub, 0o755))

	args, _, err := bubblewrapArgs(policy, "/bin/sh", sub, []string{"-c", "pwd"})
	require.NoError(t, err)

	idx := slices.Index(args, "--chdir")
	require.NotEqual(t, -1, idx, "the runner must set a working directory")
	require.Equal(t, sub, args[idx+1], "the command's own directory wins over the workspace")

	// With no explicit directory the workspace is still the default.
	args, _, err = bubblewrapArgs(policy, "/bin/sh", "", []string{"-c", "pwd"})
	require.NoError(t, err)
	idx = slices.Index(args, "--chdir")
	require.NotEqual(t, -1, idx)
	require.Equal(t, policy.Workspace, args[idx+1])
}

// TestBubblewrapArgs_ProtectedMissingParent pins that a protected path whose
// parent does not exist is masked rather than skipped. Skipping it let the
// agent create the path inside the sandbox, which for .git/hooks means a
// later `git init && git commit` executes agent-written code.
func TestBubblewrapArgs_ProtectedMissingParent(t *testing.T) {
	policy, _ := goldenPolicy(t)
	missingParent := filepath.Join(policy.Workspace, "no-such-dir", "config")
	policy.Protected = CanonicalProtectedPaths(append(policy.Protected, missingParent))

	args, _, err := bubblewrapArgs(policy, "/bin/sh", "", nil)
	require.NoError(t, err)

	found := false
	for i := 0; i+2 < len(args); i++ {
		if args[i] == "--ro-bind" && args[i+2] == missingParent {
			found = true
		}
	}
	require.True(t, found, "a protected path with a missing parent must still be masked: %v", args)
}
