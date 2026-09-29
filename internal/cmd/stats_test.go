package cmd

import (
	"context"
	"os"
	"path/filepath"
	"testing"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/projects"
	"github.com/stretchr/testify/require"
)

func TestCrawlForStatsFindsConsolidatedProjectDirs(t *testing.T) {
	t.Setenv("CRUSH_GLOBAL_DATA", resolvedTempDir(t))
	t.Setenv("CRUSH_DISABLE_PROVIDER_AUTO_UPDATE", "1")

	projectPath := filepath.Join(resolvedTempDir(t), "worktree", "pkg")
	require.NoError(t, os.MkdirAll(projectPath, 0o755))

	// A real project data directory under the consolidated projects
	// root, exactly as the default data-dir resolution produces it.
	dataDir := config.DefaultProjectDataDir(projectPath)
	conn, err := db.Connect(t.Context(), dataDir)
	require.NoError(t, err)
	require.NoError(t, conn.Close())

	require.NoError(t, projects.Register(projectPath, dataDir))

	got, err := crawlForStats(context.Background(), config.DefaultProjectsRoot())
	require.NoError(t, err)
	require.Len(t, got, 1)
	require.Equal(t, projectPath, got[0].ProjectPath)
}

// resolvedTempDir returns a temp directory with symlinks resolved so the
// consolidated projects root and the filesystem paths walked by
// crawlForStats use the same spelling (e.g. /var vs /private/var on macOS).
func resolvedTempDir(t *testing.T) string {
	t.Helper()
	resolved, err := filepath.EvalSymlinks(t.TempDir())
	require.NoError(t, err)
	return resolved
}
