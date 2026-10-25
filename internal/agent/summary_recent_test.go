package agent

import (
	"path/filepath"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestRecentlyModifiedBlock(t *testing.T) {
	t.Parallel()

	const workingDir = "/workspace"

	t.Run("empty renders nothing", func(t *testing.T) {
		t.Parallel()
		require.Empty(t, recentlyModifiedBlock(nil, workingDir))
		require.Empty(t, recentlyModifiedBlock([]string{""}, workingDir))
	})

	t.Run("paths are workspace relative", func(t *testing.T) {
		t.Parallel()

		block := recentlyModifiedBlock([]string{"/workspace/internal/agent/agent.go"}, workingDir)
		require.Contains(t, block, recentlyModifiedBlockLeadIn)
		require.Contains(t, block, "- internal/agent/agent.go")
		require.NotContains(t, block, "/workspace/", "an absolute path inside the workspace is shortened")
		require.Contains(t, block, "`view`", "the block says to re-read rather than trust the summary")
	})

	t.Run("outside the workspace keeps the absolute path", func(t *testing.T) {
		t.Parallel()

		block := recentlyModifiedBlock([]string{"/elsewhere/file.go"}, workingDir)
		require.Contains(t, block, "- /elsewhere/file.go")
	})

	t.Run("no workspace keeps the absolute path", func(t *testing.T) {
		t.Parallel()

		block := recentlyModifiedBlock([]string{"/elsewhere/file.go"}, "")
		require.Contains(t, block, "- /elsewhere/file.go")
	})

	t.Run("duplicates and the limit", func(t *testing.T) {
		t.Parallel()

		block := recentlyModifiedBlock([]string{
			"/workspace/a.go",
			"/workspace/a.go",
			"/workspace/b.go",
			"/workspace/c.go",
			"/workspace/d.go",
			"/workspace/e.go",
			"/workspace/f.go",
		}, workingDir)

		require.Equal(t, 1, strings.Count(block, "- a.go"), "a path is listed once")
		require.Equal(t, recentlyModifiedLimit, strings.Count(block, "\n- "),
			"the list is capped; the lead-in line is not an entry")
		require.NotContains(t, block, "f.go", "the cap keeps the newest entries, which the tracker already ordered")
	})
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

// The summary must not silently absorb the new block into the narrative: the
// split that feeds the next merge has to stop before it.
func TestRecentlyModifiedBlockIsANarrativeBoundary(t *testing.T) {
	t.Parallel()

	content := "## Current State\n- working\n\n" +
		recentlyModifiedBlockLeadIn + "\n\n- a.go"
	require.Equal(t, len("## Current State\n- working\n\n"), narrativeCut(content))
}

// A session with writes gets the pointer, and a session without them gets no
// empty header: "nothing was modified" is the absence of the block, not a
// heading with no entries.
func TestSummarize_RecentlyModifiedFiles(t *testing.T) {
	t.Parallel()

	t.Run("writes are listed", func(t *testing.T) {
		t.Parallel()

		env := testEnv(t)
		model := &summaryStreamModel{text: "## Current State\n- done", finishReason: fantasy.FinishReasonStop}
		sa, ok := testSessionAgent(env, model, model, "test prompt").(*sessionAgent)
		require.True(t, ok)
		sa.cfg = testConfigStore(t, env.workingDir)

		ctx := t.Context()
		sessionID := summaryTestSession(t, env)
		(*env.filetracker).RecordWrite(ctx, sessionID, filepath.Join(env.workingDir, "internal", "x.go"))
		(*env.filetracker).RecordWrite(ctx, sessionID, filepath.Join(env.workingDir, "y.go"))

		require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

		after, err := env.sessions.Get(ctx, sessionID)
		require.NoError(t, err)
		msg, err := env.messages.Get(ctx, after.SummaryMessageID)
		require.NoError(t, err)

		content := msg.Content().Text
		require.Contains(t, content, recentlyModifiedBlockLeadIn)
		require.Contains(t, content, "- internal/x.go")
		require.Contains(t, content, "- y.go")
		require.Less(t, strings.Index(content, "## Current State"), strings.Index(content, recentlyModifiedBlockLeadIn),
			"the block is appended after the model's prose")
	})

	t.Run("no writes renders no block", func(t *testing.T) {
		t.Parallel()

		env := testEnv(t)
		model := &summaryStreamModel{text: "## Current State\n- done", finishReason: fantasy.FinishReasonStop}
		sa := summaryTestAgent(env, model)

		ctx := t.Context()
		sessionID := summaryTestSession(t, env)
		require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

		after, err := env.sessions.Get(ctx, sessionID)
		require.NoError(t, err)
		msg, err := env.messages.Get(ctx, after.SummaryMessageID)
		require.NoError(t, err)
		require.NotContains(t, msg.Content().Text, recentlyModifiedBlockLeadIn)
	})
}
