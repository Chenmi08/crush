package sandbox

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/stretchr/testify/require"
)

// TestCleanupArtifacts_RemovesCreatedDirs verifies a masked path that did not
// exist is removed together with the parent directories the backend created
// for it, and that the ancestor recorded before the run is left in place.
func TestCleanupArtifacts_RemovesCreatedDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	require.NoError(t, os.MkdirAll(existing, 0o755))

	// Simulate what bwrap leaves behind: the mount point plus missing parents.
	target := filepath.Join(existing, "a", "b", ".git")
	art := recordArtifact(target)
	require.Equal(t, existing, art.existingAncestor)
	require.NoError(t, os.MkdirAll(target, 0o755))

	cleanupArtifacts([]artifact{art})()

	_, err := os.Lstat(target)
	require.True(t, os.IsNotExist(err), "the mount point must be removed")
	_, err = os.Lstat(filepath.Join(existing, "a"))
	require.True(t, os.IsNotExist(err), "the parents bwrap created must be removed")
	require.DirExists(t, existing, "a directory that already existed must survive")
}

// TestCleanupArtifacts_KeepsNonEmptyDirs verifies cleanup never deletes content
// the confined command wrote next to a mount point.
func TestCleanupArtifacts_KeepsNonEmptyDirs(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	existing := filepath.Join(root, "existing")
	require.NoError(t, os.MkdirAll(existing, 0o755))

	target := filepath.Join(existing, "created")
	art := recordArtifact(target)
	require.NoError(t, os.MkdirAll(target, 0o755))
	require.NoError(t, os.WriteFile(filepath.Join(target, "keep.txt"), []byte("x"), 0o644))

	cleanupArtifacts([]artifact{art})()

	content, err := os.ReadFile(filepath.Join(target, "keep.txt"))
	require.NoError(t, err, "a directory with content must not be removed")
	require.Equal(t, "x", string(content))
}

// TestCleanupArtifacts_IgnoresMissing verifies cleanup tolerates a mount point
// that was never created (the backend failed before starting the command).
func TestCleanupArtifacts_IgnoresMissing(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	art := recordArtifact(filepath.Join(root, "never", "created"))
	require.NotPanics(t, func() { cleanupArtifacts([]artifact{art})() })
}

// TestCleanupArtifacts_NilWhenEmpty verifies there is no cleanup work when the
// backend created nothing, which is what keeps Seatbelt's contract honest.
func TestCleanupArtifacts_NilWhenEmpty(t *testing.T) {
	t.Parallel()
	require.Nil(t, cleanupArtifacts(nil))
}

// TestPlaceholder_IsADirectory verifies the placeholder a backend binds over a
// missing protected path is a directory. An empty file is not inert: git reads
// it as a malformed gitfile and refuses to look at the repository above it.
func TestPlaceholder_IsADirectory(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	p := &Policy{TempDir: filepath.Join(root, "tmp")}

	path, err := p.protectedPlaceholder()
	require.NoError(t, err)

	info, err := os.Lstat(path)
	require.NoError(t, err)
	require.True(t, info.IsDir(), "the placeholder must be a directory, not a file")

	entries, err := os.ReadDir(path)
	require.NoError(t, err)
	require.Empty(t, entries, "the placeholder must be empty")

	// Repeated calls must be stable and must not fail on the existing dir.
	again, err := p.protectedPlaceholder()
	require.NoError(t, err)
	require.Equal(t, path, again)
}

// TestCleanupArtifacts_KeepsEmptyAncestor is the regression test for a cleanup
// bug that deleted user data: when the mount point was never created (the
// backend failed before starting the command), the walk climbed past the
// recorded ancestor and removed it, even though it was an empty directory the
// user already had.
func TestCleanupArtifacts_KeepsEmptyAncestor(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	userdir := filepath.Join(root, "userdir")
	require.NoError(t, os.MkdirAll(userdir, 0o755))

	// Nothing is created under userdir: the backend never ran.
	art := recordArtifact(filepath.Join(userdir, "never", "created"))
	require.Equal(t, userdir, art.existingAncestor)

	cleanupArtifacts([]artifact{art})()

	require.DirExists(t, userdir, "a directory the user already had must never be removed")
}

// TestCleanupArtifacts_KeepsAncestorWhenTargetVanishes covers the same path
// with a target that existed at record time and is gone by cleanup time: the
// ancestor is still not cleanup's to remove.
func TestCleanupArtifacts_KeepsAncestorWhenTargetVanishes(t *testing.T) {
	t.Parallel()
	root := t.TempDir()
	userdir := filepath.Join(root, "userdir")
	require.NoError(t, os.MkdirAll(userdir, 0o755))

	art := recordArtifact(filepath.Join(userdir, "gone"))
	require.NoError(t, os.MkdirAll(art.path, 0o755))
	require.NoError(t, os.Remove(art.path))

	cleanupArtifacts([]artifact{art})()

	require.DirExists(t, userdir)
}
