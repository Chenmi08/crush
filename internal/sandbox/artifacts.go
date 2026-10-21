package sandbox

import (
	"log/slog"
	"os"
	"path/filepath"
)

// artifact is one path a backend created on the host to express the policy,
// plus the nearest ancestor that already existed when it was recorded.
//
// A backend that masks a path which does not exist yet has to create it: the
// mount point must be a real entry. bubblewrap creates the missing parent
// directories too. Recording the pre-run ancestor bounds cleanup, so it can
// remove exactly what the backend added and nothing that was already there.
type artifact struct {
	// path is the masked path the backend created.
	path string
	// existingAncestor is the nearest ancestor of path that existed before
	// the backend ran, or "" when it could not be determined.
	existingAncestor string
}

// recordArtifact records target and its pre-run ancestor. It must run before
// the backend starts, so the ancestor reflects the filesystem as it was.
func recordArtifact(target string) artifact {
	return artifact{path: target, existingAncestor: nearestExistingAncestor(target)}
}

// nearestExistingAncestor returns the closest ancestor of path that exists,
// or "" when none does.
func nearestExistingAncestor(path string) string {
	dir := filepath.Dir(path)
	for {
		if _, err := os.Lstat(dir); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return ""
		}
		dir = parent
	}
}

// cleanupArtifacts removes the mount points a backend created to express the
// policy, so a confined command leaves the workspace as it found it.
//
// Only empty directories are removed. A masked path is bound read-only, so
// the command cannot write into it; if a directory is non-empty, something
// else now lives there and it is left alone. Removal climbs the parent
// directories the backend created and stops at the ancestor recorded before
// the run, so cleanup can never delete a directory the user already had.
func cleanupArtifacts(artifacts []artifact) func() {
	if len(artifacts) == 0 {
		return nil
	}
	return func() {
		for _, a := range artifacts {
			removeCreatedDirs(a)
		}
	}
}

// removeCreatedDirs removes a created path and the empty parents above it,
// stopping at the recorded ancestor. The ancestor is never removed: it is a
// directory the user already had, and an empty one is still theirs.
func removeCreatedDirs(art artifact) {
	stop := art.existingAncestor
	path := art.path
	for path != "" && path != stop {
		info, err := os.Lstat(path)
		if err != nil {
			// Already gone: the backend never created it. Nothing above it was
			// created for it either, so stop rather than climb into the
			// ancestor.
			return
		}
		if !info.IsDir() {
			// Not a mount point this backend created; leave it and everything
			// above it alone.
			return
		}
		if err := os.Remove(path); err != nil {
			// Non-empty (the command wrote around it) or not permitted: the
			// path is no longer ours to remove, and neither is its parent.
			if !os.IsNotExist(err) {
				slog.Debug("Could not remove sandbox mount point", "path", path, "error", err)
			}
			return
		}
		path = filepath.Dir(path)
	}
}
