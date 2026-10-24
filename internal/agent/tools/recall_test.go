package tools

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"unicode/utf8"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/db"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// recallTestEnv is a message store on a temporary database, which is all the
// tool needs: it reads the caller's session and nothing else.
func recallTestEnv(t *testing.T) message.Service {
	t.Helper()

	messages, _ := recallTestEnvWithSessions(t)
	return messages
}

// recallTestEnvWithSessions also returns the session service, for the tests
// that need two sessions.
func recallTestEnvWithSessions(t *testing.T) (message.Service, session.Service) {
	t.Helper()

	conn, err := db.Connect(t.Context(), t.TempDir())
	require.NoError(t, err)
	t.Cleanup(func() { conn.Close() })

	q := db.New(conn)
	return message.NewService(q), session.NewService(q, conn)
}

// storeToolResult writes an assistant tool call and its result, the shape a
// real turn leaves behind. The session must already exist: messages carry a
// foreign key to it.
func storeToolResult(t *testing.T, messages message.Service, sessionID, callID, name, content string, isError bool) {
	t.Helper()

	ctx := t.Context()
	_, err := messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: callID, Name: name, Input: `{}`, Finished: true},
		},
	})
	require.NoError(t, err)

	_, err = messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			message.ToolResult{ToolCallID: callID, Name: name, Content: content, IsError: isError},
		},
	})
	require.NoError(t, err)
}

// newRecallSession creates a session and returns its id.
func newRecallSession(t *testing.T, sessions session.Service, title string) string {
	t.Helper()

	sess, err := sessions.Create(t.Context(), title)
	require.NoError(t, err)
	return sess.ID
}

// recallCtx attaches the session the tool reads from.
func recallCtx(sessionID string) context.Context {
	return context.WithValue(context.Background(), SessionIDContextKey, sessionID)
}

// runRecall invokes the tool through its normal JSON entry point, so the test
// exercises the same path a model's tool call takes.
func runRecall(t *testing.T, tool fantasy.AgentTool, ctx context.Context, params RecallParams) (fantasy.ToolResponse, error) {
	t.Helper()

	input, err := json.Marshal(params)
	require.NoError(t, err)
	return tool.Run(ctx, fantasy.ToolCall{ID: "call_test", Name: RecallToolName, Input: string(input)})
}

func TestRecall_SmallResultReturnsWholeBody(t *testing.T) {
	t.Parallel()

	messages, sessions := recallTestEnvWithSessions(t)
	sessionID := newRecallSession(t, sessions, "recall")
	body := "--- FAIL: TestSummarize (0.31s)\nFAIL\n\nExit code 1"
	storeToolResult(t, messages, sessionID, "call_7f3a", "bash", body, false)

	tool := NewRecallTool(messages)
	resp, err := runRecall(t, tool, recallCtx(sessionID), RecallParams{ToolCallID: "call_7f3a"})
	require.NoError(t, err)
	require.False(t, resp.IsError, resp.Content)

	require.Contains(t, resp.Content, "tool_call_id=call_7f3a")
	require.Contains(t, resp.Content, "name=bash")
	require.Contains(t, resp.Content, "exit=1", "the failure signal belongs in the header")
	require.Contains(t, resp.Content, "bytes=")
	require.Contains(t, resp.Content, "recorded_at=")
	require.Contains(t, resp.Content, "may not reflect current state")
	require.Contains(t, resp.Content, body)
	require.NotContains(t, resp.Content, "call again with offset",
		"a result that fits in one page must not offer more")
}

func TestRecall_LimitReturnsRemainderNotice(t *testing.T) {
	t.Parallel()

	messages, sessions := recallTestEnvWithSessions(t)
	sessionID := newRecallSession(t, sessions, "recall")
	body := strings.Repeat("x", 100)
	storeToolResult(t, messages, sessionID, "call_big", "bash", body, false)

	tool := NewRecallTool(messages)
	resp, err := runRecall(t, tool, recallCtx(sessionID), RecallParams{ToolCallID: "call_big", Limit: 10})
	require.NoError(t, err)

	require.Contains(t, resp.Content, strings.Repeat("x", 10))
	require.NotContains(t, resp.Content, strings.Repeat("x", 11))
	require.Contains(t, resp.Content, "returned bytes 0-10 of 100")
	require.Contains(t, resp.Content, "offset=10")
}

func TestRecall_OffsetPastEndReturnsNotice(t *testing.T) {
	t.Parallel()

	messages, sessions := recallTestEnvWithSessions(t)
	sessionID := newRecallSession(t, sessions, "recall")
	storeToolResult(t, messages, sessionID, "call_small", "view", "hello", false)

	tool := NewRecallTool(messages)
	resp, err := runRecall(t, tool, recallCtx(sessionID), RecallParams{ToolCallID: "call_small", Offset: 100})
	require.NoError(t, err)

	require.Contains(t, resp.Content, "past the end")
	require.NotContains(t, resp.Content, "hello")
}

func TestRecall_UnknownIDIsAnError(t *testing.T) {
	t.Parallel()

	messages, sessions := recallTestEnvWithSessions(t)
	sessionID := newRecallSession(t, sessions, "recall")
	storeToolResult(t, messages, sessionID, "call_known", "bash", "ok", false)

	tool := NewRecallTool(messages)
	resp, err := runRecall(t, tool, recallCtx(sessionID), RecallParams{ToolCallID: "call_missing"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "call_missing")
	require.Contains(t, resp.Content, "no archived tool result")
}

func TestRecall_RequiresToolCallID(t *testing.T) {
	t.Parallel()

	messages, sessions := recallTestEnvWithSessions(t)
	sessionID := newRecallSession(t, sessions, "recall")
	tool := NewRecallTool(messages)

	resp, err := runRecall(t, tool, recallCtx(sessionID), RecallParams{})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "tool_call_id")
}

func TestRecall_RequiresSession(t *testing.T) {
	t.Parallel()

	messages := recallTestEnv(t)
	tool := NewRecallTool(messages)

	resp, err := runRecall(t, tool, context.Background(), RecallParams{ToolCallID: "call_x"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "session")
}

// A tool call id is only meaningful inside the session that produced it.
func TestRecall_IDFromAnotherSessionIsAnError(t *testing.T) {
	t.Parallel()

	messages, sessions := recallTestEnvWithSessions(t)
	otherID := newRecallSession(t, sessions, "other")
	storeToolResult(t, messages, otherID, "call_foreign", "bash", "secret", false)
	sessionID := newRecallSession(t, sessions, "recall")

	tool := NewRecallTool(messages)
	resp, err := runRecall(t, tool, recallCtx(sessionID), RecallParams{ToolCallID: "call_foreign"})
	require.NoError(t, err)
	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "call_foreign")
	require.NotContains(t, resp.Content, "secret")
}

func TestRecall_MultiByteRuneIsNotSplit(t *testing.T) {
	t.Parallel()

	messages, sessions := recallTestEnvWithSessions(t)
	sessionID := newRecallSession(t, sessions, "recall")
	// Each rune is 3 bytes, so a naive 4-byte slice would split one.
	body := strings.Repeat("日", 10)
	storeToolResult(t, messages, sessionID, "call_utf8", "view", body, false)

	tool := NewRecallTool(messages)
	resp, err := runRecall(t, tool, recallCtx(sessionID), RecallParams{ToolCallID: "call_utf8", Limit: 4})
	require.NoError(t, err)
	require.True(t, utf8.ValidString(resp.Content))

	// The body is the part after the header's blank line.
	_, bodyPart, found := strings.Cut(resp.Content, "(archived record; may not reflect current state)\n\n")
	require.True(t, found)
	require.Equal(t, "日", strings.TrimSuffix(bodyPart, bodyPart[strings.Index(bodyPart, "\n\n"):]))
}

func TestRecall_EmptyResult(t *testing.T) {
	t.Parallel()

	messages, sessions := recallTestEnvWithSessions(t)
	sessionID := newRecallSession(t, sessions, "recall")
	storeToolResult(t, messages, sessionID, "call_empty", "bash", "", false)

	tool := NewRecallTool(messages)
	resp, err := runRecall(t, tool, recallCtx(sessionID), RecallParams{ToolCallID: "call_empty"})
	require.NoError(t, err)
	require.Contains(t, resp.Content, "archived result is empty")
}

// The header carries the archived body verbatim, including a result that was
// itself an error. The exit status is not invented when the body has none.
func TestRecall_ErrorResultWithoutExitCode(t *testing.T) {
	t.Parallel()

	messages, sessions := recallTestEnvWithSessions(t)
	sessionID := newRecallSession(t, sessions, "recall")
	storeToolResult(t, messages, sessionID, "call_err", "bash", "boom", true)

	tool := NewRecallTool(messages)
	resp, err := runRecall(t, tool, recallCtx(sessionID), RecallParams{ToolCallID: "call_err"})
	require.NoError(t, err)
	require.Contains(t, resp.Content, "name=bash")
	require.Contains(t, resp.Content, "boom")
	require.NotContains(t, resp.Content, "exit=",
		"a body with no status line has no exit code to report")
}

// A non-bash result carries no exit status, so the header must not invent one
// even when its body happens to end in a line that looks like a status.
func TestRecall_NonBashResultHasNoExitCode(t *testing.T) {
	t.Parallel()

	messages, sessions := recallTestEnvWithSessions(t)
	sessionID := newRecallSession(t, sessions, "recall")
	body := "file contents\n\nExit code 3"
	storeToolResult(t, messages, sessionID, "call_view", "view", body, false)

	tool := NewRecallTool(messages)
	resp, err := runRecall(t, tool, recallCtx(sessionID), RecallParams{ToolCallID: "call_view"})
	require.NoError(t, err)
	require.Contains(t, resp.Content, body, "the body is returned unmodified")
	require.NotContains(t, resp.Content, "exit=",
		"only a bash result carries an exit status")
}

func TestRecallWindow(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name          string
		offset, limit int
		wantOffset    int
		wantLimit     int
	}{
		{name: "defaults", wantOffset: 0, wantLimit: recallDefaultLimit},
		{name: "negative offset clamps", offset: -5, wantOffset: 0, wantLimit: recallDefaultLimit},
		{name: "negative limit uses default", limit: -1, wantLimit: recallDefaultLimit},
		{name: "limit is capped", limit: recallMaxLimit + 1, wantLimit: recallMaxLimit},
		{name: "explicit values pass through", offset: 100, limit: 200, wantOffset: 100, wantLimit: 200},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			offset, limit := recallWindow(tt.offset, tt.limit)
			require.Equal(t, tt.wantOffset, offset)
			require.Equal(t, tt.wantLimit, limit)
		})
	}
}

func TestRecallSlice(t *testing.T) {
	t.Parallel()

	t.Run("whole string", func(t *testing.T) {
		t.Parallel()

		body, start, end, more := recallSlice("hello", 0, 100)
		require.Equal(t, "hello", body)
		require.Equal(t, 0, start)
		require.Equal(t, 5, end)
		require.False(t, more)
	})

	t.Run("offset past end yields nothing", func(t *testing.T) {
		t.Parallel()

		body, _, _, more := recallSlice("hello", 10, 10)
		require.Empty(t, body)
		require.False(t, more)
	})

	t.Run("end never splits a rune", func(t *testing.T) {
		t.Parallel()

		body, _, _, _ := recallSlice(strings.Repeat("日", 4), 0, 4)
		require.True(t, utf8.ValidString(body))
		require.Equal(t, "日", body, "3 of the 4 requested bytes fit one rune")
	})

	t.Run("start never splits a rune", func(t *testing.T) {
		t.Parallel()

		s := strings.Repeat("日", 4)
		// Offset 1 lands inside the first rune; the slice starts at the
		// next one.
		body, start, _, _ := recallSlice(s, 1, 3)
		require.Equal(t, 3, start)
		require.True(t, utf8.ValidString(body))
		require.Equal(t, "日", body)
	})
}
