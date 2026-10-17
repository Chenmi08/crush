package sandbox

import (
	"log/slog"
	"os"
)

// cleanupArtifacts removes zero-byte files a backend created to express the
// policy, so a confined command leaves the workspace as it found it. A path
// that has since gained content, or become a directory, is left alone: it is
// no longer a placeholder.
func cleanupArtifacts(_ *Policy, paths []string) func() {
	if len(paths) == 0 {
		return nil
	}
	return func() {
		for _, path := range paths {
			info, err := os.Lstat(path)
			if err != nil || info.IsDir() || info.Size() != 0 {
				continue
			}
			if err := os.Remove(path); err != nil {
				slog.Debug("Could not remove sandbox placeholder", "path", path, "error", err)
			}
		}
	}
}
