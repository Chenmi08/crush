package sandbox

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"github.com/charmbracelet/x/exp/golden"
	"github.com/stretchr/testify/require"
)

// seatbeltTestPolicy builds a policy with real paths under a temp root, so the
// generated profile names real files and the golden file can normalize the
// root.
func seatbeltTestPolicy(t *testing.T) (*Policy, string) {
	t.Helper()
	root := t.TempDir()
	workspace := filepath.Join(root, "project")
	cache := filepath.Join(root, "cache")
	extra := filepath.Join(root, "extra")
	for _, dir := range []string{workspace, cache, extra, filepath.Join(workspace, ".git", "hooks")} {
		require.NoError(t, os.MkdirAll(dir, 0o755))
	}
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

// TestSeatbeltProfile_Golden pins the generated SBPL text. The generated
// profile is the artifact: a refactor must not widen the sandbox silently,
// and every backend's rules must describe the same writable set.
func TestSeatbeltProfile_Golden(t *testing.T) {
	policy, root := seatbeltTestPolicy(t)
	profile, err := SeatbeltProfile(policy)
	require.NoError(t, err)
	golden.RequireEqual(t, []byte(normalizeRoots(profile, root)))
}

// TestSeatbeltProfile_CanonicalizesRoots verifies a root reached through a
// symlinked path still matches: the profile names the resolved path.
func TestSeatbeltProfile_CanonicalizesRoots(t *testing.T) {
	root := t.TempDir()
	real := filepath.Join(root, "real")
	require.NoError(t, os.MkdirAll(real, 0o755))
	link := filepath.Join(root, "link")
	require.NoError(t, os.Symlink(real, link))

	policy := &Policy{
		Mode:       ModeWorkspaceWrite,
		Workspace:  real,
		WriteRoots: []string{link},
	}
	profile, err := SeatbeltProfile(policy)
	require.NoError(t, err)
	require.Contains(t, profile, CanonicalPath(real))
	require.NotContains(t, profile, link, "the profile must name the resolved path")
}

// TestSeatbeltProfile_ParityWithSharedPolicy states the parity requirement: the
// writable set the generated profile describes equals the shared definition's.
func TestSeatbeltProfile_ParityWithSharedPolicy(t *testing.T) {
	policy, _ := seatbeltTestPolicy(t)
	profile, err := SeatbeltProfile(policy)
	require.NoError(t, err)

	var allowed []string
	for _, line := range strings.Split(profile, "\n") {
		if !strings.HasPrefix(line, "(allow file-write* (subpath") {
			continue
		}
		allowed = append(allowed, extractSubpath(t, line))
	}

	want := append(policy.WriteRootsFor(), "/dev/null")
	require.Equal(t, want, allowed,
		"the backend's writable set must equal the shared definition's")
}

// TestSeatbeltProfile_DeniesComeLast verifies protected paths and configured
// denials are appended after the allows, because the last match wins.
func TestSeatbeltProfile_DeniesComeLast(t *testing.T) {
	policy, _ := seatbeltTestPolicy(t)
	profile, err := SeatbeltProfile(policy)
	require.NoError(t, err)

	lastAllow := strings.LastIndex(profile, "(allow file-write*")
	require.NotEqual(t, -1, lastAllow)
	for _, deny := range policy.DenyWrites {
		idx := strings.Index(profile, `(deny file-write* (subpath "`+deny+`")`)
		require.NotEqual(t, -1, idx, "missing deny for %s", deny)
		require.Greater(t, idx, lastAllow, "deny rules must come after allows")
	}
	for _, prot := range policy.Protected {
		idx := strings.Index(profile, `(deny file-write* (subpath "`+prot+`")`)
		require.NotEqual(t, -1, idx, "missing deny for %s", prot)
		require.Greater(t, idx, lastAllow)
	}
}

// TestSeatbeltProfile_ReadsUnconfined pins that the profile does not restrict
// reads.
func TestSeatbeltProfile_ReadsUnconfined(t *testing.T) {
	policy, _ := seatbeltTestPolicy(t)
	profile, err := SeatbeltProfile(policy)
	require.NoError(t, err)
	require.Contains(t, profile, "(allow file-read*)")
	for _, line := range strings.Split(profile, "\n") {
		require.False(t, strings.HasPrefix(line, "(deny file-read"),
			"reads must not be denied: %s", line)
	}
}

// extractSubpath pulls the path out of an allow rule.
func extractSubpath(t *testing.T, line string) string {
	t.Helper()
	start := strings.Index(line, `"`)
	end := strings.LastIndex(line, `"`)
	require.Greater(t, end, start, "malformed rule: %s", line)
	return line[start+1 : end]
}
