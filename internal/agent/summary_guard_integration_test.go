package agent

import (
	"context"
	"errors"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// summaryStreamModel is a [fantasy.LanguageModel] stub that streams a summary
// body and reports the configured finish reason, so the guard paths in
// [sessionAgent.Summarize] can be exercised without a provider.
//
// A single fixed body is the common case. Setting bodies scripts one body per
// call instead, which is how a test expresses "the first compaction succeeds,
// the next one collapses", and every call's prompt is recorded so a retry can
// be told from a first attempt.
type summaryStreamModel struct {
	mu           sync.Mutex
	text         string
	bodies       []string
	finishReason fantasy.FinishReason
	// reasons scripts one finish reason per call, overriding
	// finishReason. It is how a test says "fail, fail, fail, then
	// succeed".
	reasons []fantasy.FinishReason
	prompts []string
	// histories holds the flattened message history of every call, which
	// is where the prompt layers (reminders, tombstones, recitations)
	// land: they are sent as messages, not as the prompt text.
	histories []string
	calls     int
}

// next returns the body for this call and records the prompt it was asked
// with.
func (m *summaryStreamModel) next(call fantasy.Call) string {
	m.mu.Lock()
	defer m.mu.Unlock()

	m.prompts = append(m.prompts, promptText(call.Prompt))
	m.histories = append(m.histories, fullPromptText(call.Prompt))
	body := m.text
	if m.calls < len(m.bodies) {
		body = m.bodies[m.calls]
	}
	m.calls++
	return body
}

// nextReason returns the finish reason for call n.
func (m *summaryStreamModel) nextReason(n int) fantasy.FinishReason {
	if n < len(m.reasons) {
		return m.reasons[n]
	}
	return m.finishReason
}

// callCount reports how many streams the model has served.
func (m *summaryStreamModel) callCount() int {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.calls
}

// recordedHistories returns a copy of the full request text seen so far.
func (m *summaryStreamModel) recordedHistories() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.histories...)
}

// recordedPrompts returns a copy of the prompts seen so far.
func (m *summaryStreamModel) recordedPrompts() []string {
	m.mu.Lock()
	defer m.mu.Unlock()
	return append([]string(nil), m.prompts...)
}

// promptText flattens a prompt's text parts to plain text for assertions.
func promptText(prompt fantasy.Prompt) string {
	var sb strings.Builder
	for _, msg := range prompt {
		for _, part := range msg.Content {
			if tp, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				sb.WriteString(tp.Text)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

// fullPromptText flattens a prompt including tool results, which are carried
// as their own part type and are what the tombstone replaces.
func fullPromptText(prompt fantasy.Prompt) string {
	var sb strings.Builder
	sb.WriteString(promptText(prompt))
	for _, msg := range prompt {
		for _, part := range msg.Content {
			tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			if !ok {
				continue
			}
			sb.WriteString(toolResultOutputText(tr.Output))
			sb.WriteString("\n")
		}
	}
	return sb.String()
}

// toolResultOutputText renders a tool result's output as plain text. The
// switch appears once because every test helper that flattens a prompt needs
// the same mapping from the two output shapes to a string.
func toolResultOutputText(output fantasy.ToolResultOutputContent) string {
	switch out := output.(type) {
	case fantasy.ToolResultOutputContentText:
		return out.Text
	case fantasy.ToolResultOutputContentError:
		return out.Error.Error()
	}
	return ""
}

func (m *summaryStreamModel) Generate(_ context.Context, call fantasy.Call) (*fantasy.Response, error) {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: m.next(call)}},
		FinishReason: m.finishReason,
	}, nil
}

func (m *summaryStreamModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	reason := m.nextReason(m.calls)
	m.mu.Unlock()
	text := m.next(call)
	return func(yield func(fantasy.StreamPart) bool) {
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextStart, ID: "1"}) {
			return
		}
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextDelta, ID: "1", Delta: text}) {
			return
		}
		if !yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeTextEnd, ID: "1"}) {
			return
		}
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: reason})
	}, nil
}

func (m *summaryStreamModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return &fantasy.ObjectResponse{}, nil
}

func (m *summaryStreamModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *summaryStreamModel) Provider() string { return "fake" }
func (m *summaryStreamModel) Model() string    { return "fake-model" }

// testConfigStore builds a config store rooted at dir, so the summary path can
// query a real working tree.
func testConfigStore(t *testing.T, dir string) *config.ConfigStore {
	t.Helper()

	store, err := config.Init(dir, "", false)
	require.NoError(t, err)
	return store
}

func summaryTestAgent(env fakeEnv, model fantasy.LanguageModel) *sessionAgent {
	return testSessionAgent(env, model, model, "test prompt").(*sessionAgent)
}

// summaryTestSession creates a session with enough history to summarize and
// returns it.
func summaryTestSession(t *testing.T, env fakeEnv) string {
	t.Helper()

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "summary session")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "fix the failing test"}},
	})
	require.NoError(t, err)

	sess, err = env.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	return sess.ID
}

func TestSummarize_TruncatedSummaryLeavesAnchorUntouched(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{
		text:         "## Current State\n- partial",
		finishReason: fantasy.FinishReasonLength,
	}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "summary session")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "fix the failing test"}},
	})
	require.NoError(t, err)

	// Pretend a previous, complete summary is already authoritative.
	sess, err = env.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	sess.SummaryMessageID = "previous-summary-id"
	sess, err = env.sessions.Save(ctx, sess)
	require.NoError(t, err)
	require.Equal(t, "previous-summary-id", sess.SummaryMessageID)

	err = sa.Summarize(ctx, sess.ID, fantasy.ProviderOptions{}, nil)
	require.ErrorIs(t, err, errSummaryTruncated)

	// The anchor must not have moved: the previous summary stays
	// authoritative, so the session keeps working.
	after, err := env.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.Equal(t, "previous-summary-id", after.SummaryMessageID)
}

func TestSummarize_TruncatedSummaryIsPersistedAsError(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{
		text:         "## Current State\n- partial",
		finishReason: fantasy.FinishReasonLength,
	}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	err := sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil)
	require.ErrorIs(t, err, errSummaryTruncated)

	require.NoError(t, env.messages.FlushAll(ctx))
	msgs, err := env.messages.List(ctx, sessionID)
	require.NoError(t, err)

	var summaryMsg *message.Message
	for i := range msgs {
		if msgs[i].IsSummaryMessage {
			summaryMsg = &msgs[i]
			break
		}
	}
	require.NotNil(t, summaryMsg, "the failed summary message should still be persisted")
	require.Equal(t, message.FinishReasonError, summaryMsg.FinishReason())
	finish := summaryMsg.FinishPart()
	require.NotNil(t, finish)
	require.Equal(t, "Summarization Error", finish.Message)
	require.Contains(t, finish.Details, "output limit")
}

func TestSummarize_CompleteSummaryMovesAnchor(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{
		text:         "## Current State\n- done",
		finishReason: fantasy.FinishReasonStop,
	}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, after.SummaryMessageID)

	msg, err := env.messages.Get(ctx, after.SummaryMessageID)
	require.NoError(t, err)
	require.True(t, msg.IsSummaryMessage)
	require.Equal(t, message.FinishReasonEndTurn, msg.FinishReason())
}

func TestSummarize_NoFinishReasonIsAccepted(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	// Some providers do not report a finish reason at all.
	model := &summaryStreamModel{text: "## Current State\n- done"}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, after.SummaryMessageID)
}

func TestSummarize_StripsAnalysisBlockBeforePersisting(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	body := "## Current State\n- done\n\n## User Messages\n- \"fix the test\""
	model := &summaryStreamModel{
		text:         "<analysis>\nlet me think about this\n</analysis>\n\n" + body,
		finishReason: fantasy.FinishReasonStop,
	}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, after.SummaryMessageID)

	msg, err := env.messages.Get(ctx, after.SummaryMessageID)
	require.NoError(t, err)
	content := msg.Content().Text
	require.NotContains(t, content, "<analysis>")
	require.NotContains(t, content, "let me think about this")

	// The narrative is preserved verbatim; the code-generated manifest may
	// follow it.
	narrative, _, _ := strings.Cut(content, manifestLeadIn)
	require.Equal(t, body, strings.TrimSpace(narrative))
	// The seven authored headings survive the strip.
	for _, heading := range []string{"## Current State", "## User Messages"} {
		require.Contains(t, content, heading)
	}
}

func TestSummarize_AppendsManifestAfterModelProse(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	body := "## Current State\n- the failing test is `TestSummarize`"
	model := &summaryStreamModel{text: body, finishReason: fantasy.FinishReasonStop}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "summary session")
	require.NoError(t, err)

	// One failing bash call, one edited file, one user message.
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "fix the failing test"}},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{
				ID:       "call_test",
				Name:     "bash",
				Input:    `{"command":"go test ./internal/agent/..."}`,
				Finished: true,
			},
			message.ToolCall{
				ID:       "call_edit",
				Name:     "edit",
				Input:    `{"file_path":"internal/agent/agent.go"}`,
				Finished: true,
			},
		},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{
				ToolCallID: "call_test",
				Name:       "bash",
				Content:    "--- FAIL: TestSummarize (0.31s)\nFAIL\n\nExit code 1",
			},
			message.ToolResult{
				ToolCallID: "call_edit",
				Name:       "edit",
				Content:    "ok",
			},
		},
	})
	require.NoError(t, err)

	require.NoError(t, sa.Summarize(ctx, sess.ID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	msg, err := env.messages.Get(ctx, after.SummaryMessageID)
	require.NoError(t, err)

	content := msg.Content().Text
	require.Contains(t, content, "<context_manifest")
	require.Contains(t, content, `cmd="go test ./internal/agent/..."`)
	require.Contains(t, content, "path=internal/agent/agent.go ops=edit edits=1")

	// The model's prose precedes the code-generated block: the summary the
	// model wrote is not rewritten, the facts are appended to it. The
	// lead-in marks the manifest as data, not instructions.
	require.Less(t, strings.Index(content, body), strings.Index(content, manifestLeadIn))
	require.Less(t, strings.Index(content, manifestLeadIn), strings.Index(content, "<context_manifest"))
}

// The user-visible payoff: a session that edited files through shell commands
// still ends up with a file list, because the list comes from git rather than
// from tool-call arguments.
func TestSummarize_ListsFilesEditedThroughShell(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{text: "## Current State\n- edited", finishReason: fantasy.FinishReasonStop}
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
		Cfg:      testConfigStore(t, env.workingDir),
	}).(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "shell edits")
	require.NoError(t, err)

	// Create a repository with one committed file, then change it the way an
	// agent working through a shell would: no file tool, no path in any tool
	// call input.
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "a.go"), []byte("package a\n"), 0o644))
	for _, args := range [][]string{
		{"init", "--quiet"},
		{"add", "a.go"},
		{"-c", "user.email=t@t", "-c", "user.name=t", "commit", "--quiet", "-m", "init"},
	} {
		cmd := exec.Command("git", args...)
		cmd.Dir = env.workingDir
		require.NoError(t, cmd.Run(), "git %v", args)
	}
	require.NoError(t, os.WriteFile(filepath.Join(env.workingDir, "a.go"), []byte("package a // changed\n"), 0o644))

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "change a.go"}},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_sed", Name: "bash", Input: `{"command":"sed -i s/a/b/ a.go"}`, Finished: true},
		},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call_sed", Name: "bash", Content: ""}},
	})
	require.NoError(t, err)

	require.NoError(t, sa.Summarize(ctx, sess.ID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	msg, err := env.messages.Get(ctx, after.SummaryMessageID)
	require.NoError(t, err)

	content := msg.Content().Text
	require.Contains(t, content, "path=a.go", "a shell-edited file must appear in the summary")
	require.Contains(t, content, "src=git state=modified")
}
