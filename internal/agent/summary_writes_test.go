package agent

import (
	"path/filepath"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestSummaryWrites(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := summaryTestAgent(env, &summaryStreamModel{})
	sessionID := summaryTestSession(t, env)

	ctx := t.Context()
	inside := filepath.Join(env.workingDir, "internal", "x.go")
	outside := filepath.Join(t.TempDir(), "y.go")
	(*env.filetracker).RecordWrite(ctx, sessionID, inside)
	(*env.filetracker).RecordWrite(ctx, sessionID, outside)

	writes := sa.summaryWrites(ctx, sessionID, env.workingDir)
	require.Len(t, writes, 2)

	byPath := make(map[string]manifestFile, len(writes))
	for _, write := range writes {
		byPath[write.Path] = write
	}
	require.Contains(t, byPath, "internal/x.go",
		"a path inside the workspace is shortened")
	require.Contains(t, byPath, outside,
		"a path outside the workspace keeps its absolute form")
	require.NotZero(t, byPath["internal/x.go"].Last,
		"the ledger supplies the write time")
}

// The write ledger is the manifest's source for files changed through a file
// tool; the standalone recently-modified block is gone.
func TestSummarize_ManifestListsLedgerWrites(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{text: "## Current State\n- done", finishReason: fantasy.FinishReasonStop}
	sa := summaryTestAgent(env, model)
	sa.cfg = testConfigStore(t, env.workingDir)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)
	(*env.filetracker).RecordWrite(ctx, sessionID, filepath.Join(env.workingDir, "internal", "x.go"))

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	msg, err := env.messages.Get(ctx, after.SummaryMessageID)
	require.NoError(t, err)

	content := msg.Content().Text
	require.Contains(t, content, "files:")
	require.Contains(t, content, "path=internal/x.go")
	require.Contains(t, content, "src=tool")
	require.NotContains(t, content, recentlyModifiedBlockLeadIn,
		"the write list lives in the manifest, not a second block")
}

func TestDisplayPath(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		path     string
		dir      string
		expected string
	}{
		{name: "inside", path: "/ws/a/b.go", dir: "/ws", expected: "a/b.go"},
		{name: "the root itself", path: "/ws", dir: "/ws", expected: "."},
		{name: "sibling prefix", path: "/ws-evil/a.go", dir: "/ws", expected: "/ws-evil/a.go"},
		{name: "parent", path: "/other/a.go", dir: "/ws", expected: "/other/a.go"},
		{name: "no dir", path: "/ws/a.go", dir: "", expected: "/ws/a.go"},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.expected, displayPath(tt.path, tt.dir))
		})
	}
}
