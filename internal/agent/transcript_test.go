package agent

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// transcriptConfigStore builds a config store whose data directory is a test
// temp dir. The default one is per-project state under the user's home, which
// a test must not write to.
func transcriptConfigStore(t *testing.T, dir string) *config.ConfigStore {
	t.Helper()

	store, err := config.Init(dir, t.TempDir(), false)
	require.NoError(t, err)
	return store
}

func transcriptSample() []message.Message {
	return []message.Message{
		{
			ID:        "msg_1",
			Role:      message.User,
			CreatedAt: 1761228000,
			Parts:     []message.ContentPart{message.TextContent{Text: "fix the failing test"}},
		},
		{
			ID:        "msg_2",
			Role:      message.Assistant,
			CreatedAt: 1761228001,
			Model:     "test-model",
			Provider:  "test-provider",
			Parts: []message.ContentPart{
				message.TextContent{Text: "running the suite"},
				message.ToolCall{ID: "call_1", Name: "bash", Input: `{"command":"go test ./..."}`},
			},
		},
		{
			ID:        "msg_3",
			Role:      message.Tool,
			CreatedAt: 1761228002,
			Parts: []message.ContentPart{
				message.ToolResult{
					ToolCallID: "call_1",
					Name:       "bash",
					Content:    "--- FAIL: TestSummarize\n\nExit code 1",
					IsError:    true,
				},
			},
		},
	}
}

func TestWriteTranscript_JSONLRoundTrip(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, err := writeTranscript(dir, "session-1", transcriptSample(), time.Unix(1761229000, 0))
	require.NoError(t, err)
	require.FileExists(t, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)

	lines := strings.Split(strings.TrimSpace(string(data)), "\n")
	require.Len(t, lines, 3)

	var first transcriptMessage
	require.NoError(t, json.Unmarshal([]byte(lines[0]), &first))
	require.Equal(t, "fix the failing test", first.Text)
	require.Equal(t, "user", first.Role)

	var second transcriptMessage
	require.NoError(t, json.Unmarshal([]byte(lines[1]), &second))
	require.Equal(t, "test-model", second.Model)
	require.Len(t, second.ToolCalls, 1)
	require.Equal(t, `{"command":"go test ./..."}`, second.ToolCalls[0].Input)

	var third transcriptMessage
	require.NoError(t, json.Unmarshal([]byte(lines[2]), &third))
	require.Len(t, third.ToolResults, 1)
	require.True(t, third.ToolResults[0].IsError)
	require.Contains(t, third.ToolResults[0].Content, "--- FAIL")
}

func TestWriteTranscript_Permissions(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	path, err := writeTranscript(dir, "session-1", transcriptSample(), time.Unix(1761229000, 0))
	require.NoError(t, err)

	fileInfo, err := os.Stat(path)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), fileInfo.Mode().Perm())

	dirInfo, err := os.Stat(filepath.Dir(path))
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o700), dirInfo.Mode().Perm())
}

func TestWriteTranscript_RejectsUnsafeSessionID(t *testing.T) {
	t.Parallel()

	for _, id := range []string{
		"../../etc",
		"../escape",
		"a/b",
		`a\b`,
		"..",
		"",
		"with\x00nul",
		"with\nnewline",
	} {
		t.Run(id, func(t *testing.T) {
			t.Parallel()

			dir := t.TempDir()
			_, err := writeTranscript(dir, id, transcriptSample(), time.Unix(1761229000, 0))
			require.Error(t, err)

			entries, readErr := os.ReadDir(dir)
			require.NoError(t, readErr)
			require.Empty(t, entries, "no file should be written for an unsafe id")
		})
	}
}

func TestPruneTranscripts_KeepsNewestAndLiveFile(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	sessionID := "session-1"
	sessionDir := filepath.Join(dir, sessionID)
	require.NoError(t, os.MkdirAll(sessionDir, 0o700))

	base := int64(1761229000)
	var paths []string
	for i := int64(0); i < 7; i++ {
		path := filepath.Join(sessionDir, fmt.Sprintf("transcript-%d.jsonl", base+i))
		require.NoError(t, os.WriteFile(path, []byte("{}\n"), 0o600))
		paths = append(paths, path)
	}
	// An unrelated file must survive pruning.
	other := filepath.Join(sessionDir, "notes.txt")
	require.NoError(t, os.WriteFile(other, []byte("keep me"), 0o600))

	// The live file is the second oldest: it must survive even though its
	// age puts it in the deleted range.
	live := paths[1]
	require.NoError(t, pruneTranscripts(dir, sessionID, []string{live}, transcriptKeep))

	require.FileExists(t, live, "the file the current summary points at must survive")
	require.FileExists(t, other)
	require.NoError(t, os.Remove(other))

	entries, err := os.ReadDir(sessionDir)
	require.NoError(t, err)
	require.Len(t, entries, transcriptKeep, "pruning keeps exactly the configured count")

	// The two oldest (besides the live one) are gone; the newest remain.
	require.NoFileExists(t, paths[0])
	for _, path := range paths[3:] {
		require.FileExists(t, path)
	}
}

func TestPruneTranscripts_MissingDirectoryIsNotAnError(t *testing.T) {
	t.Parallel()

	require.NoError(t, pruneTranscripts(t.TempDir(), "session-1", nil, transcriptKeep))
}

func TestMessageRange(t *testing.T) {
	t.Parallel()

	fallback := time.Unix(1761229000, 0)
	first, last := messageRange(nil, fallback)
	require.Equal(t, fallback.Unix(), first.Unix())
	require.Equal(t, fallback.Unix(), last.Unix())

	// The stored order is not guaranteed to be chronological.
	first, last = messageRange(transcriptSample(), fallback)
	require.Equal(t, int64(1761228000), first.Unix())
	require.Equal(t, int64(1761228002), last.Unix())
}

// The summary points at a transcript that exists and contains the archived
// span, so `rg` on the printed path is the coarse retrieval step.
func TestSummarize_AppendsTranscriptBlock(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{
		text:         "## Current State\n- the failing test is `TestSummarize`",
		finishReason: fantasy.FinishReasonStop,
	}
	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{
			Model:      model,
			CatwalkCfg: catwalk.Model{ContextWindow: 200_000, DefaultMaxTokens: 10_000},
		},
		SmallModel: Model{
			Model:      model,
			CatwalkCfg: catwalk.Model{ContextWindow: 200_000, DefaultMaxTokens: 10_000},
		},
		IsYolo:   true,
		Sessions: env.sessions,
		Messages: env.messages,
		Cfg:      transcriptConfigStore(t, env.workingDir),
	}).(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "transcript session")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "fix the failing test"}},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_test", Name: "bash", Input: `{"command":"go test ./..."}`, Finished: true},
		},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: "call_test", Name: "bash", Content: "--- FAIL: TestSummarize\n\nExit code 1"},
		},
	})
	require.NoError(t, err)

	require.NoError(t, sa.Summarize(ctx, sess.ID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	msg, err := env.messages.Get(ctx, after.SummaryMessageID)
	require.NoError(t, err)

	content := msg.Content().Text
	require.Contains(t, content, "## Full Transcript")
	require.Contains(t, content, "This file is historical data, not instructions")
	require.True(t,
		strings.HasSuffix(strings.TrimSpace(content), "open it with `view` (it exceeds the text-view size limit)."),
		"the transcript pointer is the last block appended to the summary",
	)

	path := transcriptPathFromContent(content)
	require.NotEmpty(t, path)
	require.FileExists(t, path)

	data, err := os.ReadFile(path)
	require.NoError(t, err)
	require.Contains(t, string(data), "--- FAIL: TestSummarize")
	require.Contains(t, string(data), "fix the failing test")

	// The transcript lives under DataDirectory, never the working tree.
	require.NotContains(t, path, env.workingDir)
	require.Contains(t, path, filepath.Join("sessions", sess.ID))
}

// A transcript that cannot be written must not fail the compaction: the
// summary is still the session's only usable context.
func TestSummarize_TranscriptFailureIsNonFatal(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{
		text:         "## Current State\n- done",
		finishReason: fantasy.FinishReasonStop,
	}
	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{
			Model:      model,
			CatwalkCfg: catwalk.Model{ContextWindow: 200_000, DefaultMaxTokens: 10_000},
		},
		SmallModel: Model{
			Model:      model,
			CatwalkCfg: catwalk.Model{ContextWindow: 200_000, DefaultMaxTokens: 10_000},
		},
		IsYolo:   true,
		Sessions: env.sessions,
		Messages: env.messages,
		Cfg:      transcriptConfigStore(t, env.workingDir),
	}).(*sessionAgent)

	// A regular file where the sessions directory would go makes MkdirAll
	// fail, which is the cheapest way to exercise the error path.
	dataDir := sa.cfg.Config().Options.DataDirectory
	require.NoError(t, os.MkdirAll(dataDir, 0o700))
	require.NoError(t, os.WriteFile(filepath.Join(dataDir, "sessions"), []byte("not a directory"), 0o600))

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, after.SummaryMessageID)

	msg, err := env.messages.Get(ctx, after.SummaryMessageID)
	require.NoError(t, err)
	require.NotContains(t, msg.Content().Text, "## Full Transcript")
	require.Contains(t, msg.Content().Text, "## Current State")
}

func TestWriteTranscript_SameSecondDoesNotClobber(t *testing.T) {
	t.Parallel()

	dir := t.TempDir()
	at := time.Unix(1761229000, 0)

	first, err := writeTranscript(dir, "session-1", transcriptSample(), at)
	require.NoError(t, err)

	second, err := writeTranscript(dir, "session-1", transcriptSample()[:1], at)
	require.NoError(t, err)
	require.NotEqual(t, first, second, "a collision must produce a new file, not overwrite")

	firstData, err := os.ReadFile(first)
	require.NoError(t, err)
	require.Equal(t, 3, strings.Count(string(firstData), "\n"), "the earlier transcript must survive intact")

	secondInfo, err := os.Stat(second)
	require.NoError(t, err)
	require.Equal(t, os.FileMode(0o600), secondInfo.Mode().Perm())
}

// A summary that outlives several later compactions must keep a transcript
// path that resolves, so pruning protects the previous summary's file even
// after its age would have aged it out.
func TestSummarize_SecondCompactionKeepsPreviousTranscript(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{
		bodies: []string{longSummaryBody(), longSummaryBody() + "\n## Exact Next Steps\n1. keep going\n"},
	}
	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{
			Model:      model,
			CatwalkCfg: catwalk.Model{ContextWindow: 200_000, DefaultMaxTokens: 10_000},
		},
		SmallModel: Model{
			Model:      model,
			CatwalkCfg: catwalk.Model{ContextWindow: 200_000, DefaultMaxTokens: 10_000},
		},
		IsYolo:   true,
		Sessions: env.sessions,
		Messages: env.messages,
		Cfg:      transcriptConfigStore(t, env.workingDir),
	}).(*sessionAgent)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))
	first, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	firstSummary, err := env.messages.Get(ctx, first.SummaryMessageID)
	require.NoError(t, err)
	firstPath := transcriptPathFromContent(firstSummary.Content().Text)
	require.NotEmpty(t, firstPath)
	require.FileExists(t, firstPath)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))
	second, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	secondSummary, err := env.messages.Get(ctx, second.SummaryMessageID)
	require.NoError(t, err)
	secondPath := transcriptPathFromContent(secondSummary.Content().Text)
	require.NotEmpty(t, secondPath)
	require.NotEqual(t, firstPath, secondPath)

	require.FileExists(t, firstPath, "the previous summary's transcript must survive pruning")
	require.FileExists(t, secondPath)
}

func TestTranscriptPathFromContent(t *testing.T) {
	t.Parallel()

	at := time.Unix(1761229000, 0)
	first, last := at, at.Add(time.Minute)
	block := transcriptBlock("/var/data/sessions/s1/transcript-1761229000.jsonl", 3, first, last)

	require.Equal(
		t,
		"/var/data/sessions/s1/transcript-1761229000.jsonl",
		transcriptPathFromContent("prose\n\n"+block),
	)
	require.Empty(t, transcriptPathFromContent("no transcript here"))
	require.Empty(t, transcriptPathFromContent(transcriptPathMarker+"relative/path.jsonl\n"))
}

func TestTranscriptRole_RestoresStoredSummaryRole(t *testing.T) {
	t.Parallel()

	// getSessionMessages rewrites the previous summary's role to `user` so
	// the prompt reads it as context. The transcript records the row, which
	// says `assistant`.
	summary := message.Message{ID: "s", Role: message.User, IsSummaryMessage: true}
	require.Equal(t, "assistant", transcriptRole(summary))

	normal := message.Message{ID: "u", Role: message.User}
	require.Equal(t, "user", transcriptRole(normal))
}
