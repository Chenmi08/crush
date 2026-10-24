package agent

import (
	"context"
	"strings"
	"sync"
	"testing"
	"unicode/utf8"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// historyText flattens the text of a prepared history so a test can assert on
// what the model would read without caring about part boundaries.
func historyText(history []fantasy.Message) string {
	var sb strings.Builder
	for _, msg := range history {
		for _, part := range msg.Content {
			if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
				sb.WriteString(text.Text)
				sb.WriteString("\n")
			}
		}
	}
	return sb.String()
}

// lastText returns the text of the last message in a history, which is where
// the recitation belongs.
func lastText(t *testing.T, history []fantasy.Message) string {
	t.Helper()
	require.NotEmpty(t, history)
	var sb strings.Builder
	for _, part := range history[len(history)-1].Content {
		if text, ok := fantasy.AsMessagePart[fantasy.TextPart](part); ok {
			sb.WriteString(text.Text)
		}
	}
	return sb.String()
}

func TestPreparePrompt_HeadReminderListsRealTodos(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "system").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "todos")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "refactor the parser"}},
	})
	require.NoError(t, err)
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	todos := []session.Todo{
		{Content: "Read the parser", Status: session.TodoStatusCompleted, ActiveForm: "Reading the parser"},
		{Content: "Split the lexer", Status: session.TodoStatusInProgress, ActiveForm: "Splitting the lexer"},
		{Content: "Run the tests", Status: session.TodoStatusPending, ActiveForm: "Running the tests"},
	}

	history, _ := sa.preparePrompt(msgs, promptOptions{Todos: todos, Recite: true}, true)

	head := historyText(history[:1])
	require.Contains(t, head, "[completed] Read the parser")
	require.Contains(t, head, "[in_progress] Split the lexer")
	require.Contains(t, head, "Splitting the lexer")
	require.Contains(t, head, "[pending] Run the tests")
	require.NotContains(t, head, "your todo list is currently empty",
		"a session with a list must not be told the list is empty")
}

// TestPreparePrompt_EmptyTodosKeepsOriginalReminder pins the historical text
// verbatim: it is correct when there is no list, and changing it would be a
// silent behavior change for every session that never uses the tool.
func TestPreparePrompt_EmptyTodosKeepsOriginalReminder(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "system").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "no-todos")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "just answer"}},
	})
	require.NoError(t, err)
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := sa.preparePrompt(msgs, promptOptions{Recite: true}, true)

	require.Contains(t, historyText(history[:1]), todoReminderEmpty)
}

// TestPreparePrompt_TailRecitationHasGoalAndTask covers the tail half of the
// layer: the goal from the newest user message and the in-progress task.
func TestPreparePrompt_TailRecitationHasGoalAndTask(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "system").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "recitation")
	require.NoError(t, err)
	for _, text := range []string{"first request", "second request"} {
		_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
			Role:  message.User,
			Parts: []message.ContentPart{message.TextContent{Text: text}},
		})
		require.NoError(t, err)
	}
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	todos := []session.Todo{
		{Content: "Write the test", Status: session.TodoStatusInProgress, ActiveForm: "Writing the test"},
	}
	history, _ := sa.preparePrompt(msgs, promptOptions{Todos: todos, Recite: true}, true)

	tail := lastText(t, history)
	require.Equal(t, fantasy.MessageRoleUser, history[len(history)-1].Role)
	require.Contains(t, tail, "second request", "the newest goal is the one being worked on")
	require.NotContains(t, tail, "first request")
	require.Contains(t, tail, "Writing the test")
	require.Contains(t, tail, "not a new request", "the recitation must not read as a fresh instruction")
}

// TestPreparePrompt_TailRecitationWithoutTodos still carries the goal: a
// session that never used the todo tool is the common case, and the
// recitation is not conditional on the list existing.
func TestPreparePrompt_TailRecitationWithoutTodos(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "system").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "no-todos")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "ship the fix"}},
	})
	require.NoError(t, err)
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := sa.preparePrompt(msgs, promptOptions{Recite: true}, true)

	require.Contains(t, historyText(history[:1]), todoReminderEmpty)
	tail := lastText(t, history)
	require.Contains(t, tail, "ship the fix")
	require.NotContains(t, tail, "Current task:")
}

// TestPreparePrompt_RecitationIsLast guards the alternation invariant: the
// recitation must be the final element or a provider that requires the model
// to see it last (and never after a trailing assistant or tool turn) would
// either ignore it or reject the request.
func TestPreparePrompt_RecitationIsLast(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "system").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "ordering")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "do the work"}},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_1", Name: "view", Input: `{"file_path":"a.go"}`, Finished: true},
		},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call_1", Name: "view", Content: "package a"}},
	})
	require.NoError(t, err)
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := sa.preparePrompt(msgs, promptOptions{Todos: []session.Todo{
		{Content: "Check ordering", Status: session.TodoStatusInProgress, ActiveForm: "Checking ordering"},
	}, Recite: true}, true)

	require.Equal(t, fantasy.MessageRoleUser, history[len(history)-1].Role)
	require.Contains(t, lastText(t, history), "Current goal: do the work")

	// The tool call and its result are still adjacent, with the recitation
	// only after them.
	requireToolCallAdjacency(t, history)
}

// TestPreparePrompt_SubAgentGetsNoReminder keeps the head block out of
// sub-agent prompts: a sub-agent owns its own task and has no todo list, so
// the reminder would describe a session that is not its own.
func TestPreparePrompt_SubAgentGetsNoReminder(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := NewSessionAgent(SessionAgentOptions{
		IsSubAgent: true,
		Sessions:   env.sessions,
		Messages:   env.messages,
	}).(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "sub")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "sub task"}},
	})
	require.NoError(t, err)
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := sa.preparePrompt(msgs, promptOptions{
		Todos: []session.Todo{
			{Content: "Should not appear", Status: session.TodoStatusPending},
		},
		Recite: !sa.isSubAgent,
		Goal:   "sub task",
	}, true)

	text := historyText(history)
	require.NotContains(t, text, "todo list is currently empty")
	require.NotContains(t, text, "Should not appear",
		"a sub-agent owns its own task and has no todo list to report")
	require.NotContains(t, text, "Current goal:",
		"a sub-agent has its own prompt and is not resuming the parent's work")
}

// TestPreparePrompt_NoGoalAndNoTodosAddsNoRecitation covers the degenerate
// case: a tool-only continuation has nothing to recite, and an empty block
// would read as a new instruction.
func TestPreparePrompt_NoGoalAndNoTodosAddsNoRecitation(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "system").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "tool-only")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.Tool,
		Parts: []message.ContentPart{message.ToolResult{ToolCallID: "call_1", Name: "view", Content: "package a"}},
	})
	require.NoError(t, err)
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := sa.preparePrompt(msgs, promptOptions{Recite: true}, true)

	require.Equal(t, 1, len(history), "only the head reminder belongs in an empty history")
	require.Contains(t, historyText(history), "your todo list is currently empty")
}

func TestGoalRecitation_ClipsLongGoal(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("é", goalRecitationLimit)
	msgs := []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: long}},
	}}

	got := latestUserGoal(msgs)
	require.LessOrEqual(t, len(got), goalRecitationLimit)
	require.True(t, strings.HasSuffix(got, "…"))
	require.True(t, utf8.ValidString(got), "clipping must not split a rune")
}

// TestGoalRecitation_FlattensMultilineGoal keeps the framing next to the text
// it frames: the verbatim copy is already in the history above.
func TestGoalRecitation_FlattensMultilineGoal(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "first line\n\n  second   line"}},
	}}

	require.Equal(t, "first line second line", latestUserGoal(msgs))
}

func TestGoalRecitation_SkipsEmptyUserMessages(t *testing.T) {
	t.Parallel()

	msgs := []message.Message{
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "the real goal"}}},
		{Role: message.Assistant, Parts: []message.ContentPart{message.TextContent{Text: "ok"}}},
		{Role: message.User, Parts: []message.ContentPart{message.TextContent{Text: "   "}}},
	}

	require.Equal(t, "the real goal", latestUserGoal(msgs))
}

func TestCurrentTodo(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		todos []session.Todo
		want  string
	}{
		{
			name: "in progress wins over pending",
			todos: []session.Todo{
				{Content: "later", Status: session.TodoStatusPending},
				{Content: "now", Status: session.TodoStatusInProgress, ActiveForm: "Doing now"},
			},
			want: "Doing now",
		},
		{
			name: "falls back to the first pending",
			todos: []session.Todo{
				{Content: "done", Status: session.TodoStatusCompleted},
				{Content: "first pending", Status: session.TodoStatusPending},
				{Content: "second pending", Status: session.TodoStatusPending},
			},
			want: "first pending",
		},
		{
			name: "content is used when there is no active form",
			todos: []session.Todo{
				{Content: "no active form", Status: session.TodoStatusInProgress},
			},
			want: "no active form",
		},
		{
			name: "an all-completed list names nothing",
			todos: []session.Todo{
				{Content: "done", Status: session.TodoStatusCompleted},
			},
		},
		{
			name: "an empty list names nothing",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, currentTodo(tt.todos))
		})
	}
}

func TestTodoReminder(t *testing.T) {
	t.Parallel()

	t.Run("empty list is the historical text", func(t *testing.T) {
		t.Parallel()
		// Asserted against a literal rather than the constant, so an edit
		// to the constant cannot silently change what a session with no
		// list is told.
		got := todoReminder(nil)
		require.Contains(t, got, "This is a reminder that your todo list is currently empty.")
		require.Contains(t, got, `please use the "todos" tool to create one.`)
		require.Contains(t, got, "If not, please feel free to ignore.")
		require.Equal(t, todoReminderEmpty, got)
	})

	t.Run("list entries carry status and active form", func(t *testing.T) {
		t.Parallel()
		got := todoReminder([]session.Todo{
			{Content: "a", Status: session.TodoStatusInProgress, ActiveForm: "Doing a"},
			{Content: "b", Status: session.TodoStatusPending},
		})
		require.Contains(t, got, "[in_progress] a (Doing a)")
		require.Contains(t, got, "[pending] b")
		require.NotContains(t, got, "()")
	})
}

func TestClipToBytes(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		max  int
		want string
	}{
		{name: "fits", in: "short", max: 10, want: "short"},
		{name: "zero budget", in: "short", max: 0},
		{name: "cuts on a rune boundary", in: "aébc", max: 4, want: "a…"},
		{name: "budget reserved for the marker", in: "abcd", max: 3, want: "…"},
		{name: "trims trailing space before the ellipsis", in: "ab cdef", max: 6, want: "ab…"},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, clipToBytes(tt.in, tt.max))
		})
	}
}

// TestSummarize_HasNoDuplicateTodoBlock guards the layering decision: the
// summary prompt already carries the todo list through buildSummaryPrompt, so
// the summarizer's history must not gain a second copy from the reminder and
// must not gain a recitation (it is not resuming work).
func TestSummarize_HasNoDuplicateTodoBlock(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{text: "## Current State\n- done", finishReason: fantasy.FinishReasonStop}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "summarize todos")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "fix the test"}},
	})
	require.NoError(t, err)
	sess, err = env.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	sess.Todos = []session.Todo{
		{Content: "Run the suite", Status: session.TodoStatusInProgress, ActiveForm: "Running the suite"},
	}
	_, err = env.sessions.Save(ctx, sess)
	require.NoError(t, err)

	require.NoError(t, sa.Summarize(ctx, sess.ID, fantasy.ProviderOptions{}, nil))

	require.NotEmpty(t, model.recordedPrompts())
	for _, prompt := range model.recordedPrompts() {
		require.NotContains(t, prompt, "Current goal:",
			"the summarizer is not resuming the work, so a recitation is noise")
		require.Equal(t, 1, strings.Count(prompt, "Run the suite"),
			"the todo list must reach the summarizer exactly once, via buildSummaryPrompt")
	}
}

// promptCapturingModel records the messages fantasy actually sends, so a test
// can assert on the assembled request rather than on preparePrompt's return
// value in isolation.
type promptCapturingModel struct {
	finishStreamModel
	mu       sync.Mutex
	requests []fantasy.Prompt
}

func (m *promptCapturingModel) Stream(_ context.Context, call fantasy.Call) (fantasy.StreamResponse, error) {
	m.mu.Lock()
	m.requests = append(m.requests, call.Prompt)
	text := m.text
	m.mu.Unlock()
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
		yield(fantasy.StreamPart{Type: fantasy.StreamPartTypeFinish, FinishReason: fantasy.FinishReasonStop})
	}, nil
}

func (m *promptCapturingModel) lastRequest(t *testing.T) fantasy.Prompt {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	require.NotEmpty(t, m.requests)
	return m.requests[len(m.requests)-1]
}

// runRequest returns the captured request that carries the turn's prompt. The
// title generator runs on a detached goroutine against the same mock model, so
// "the last request" can be the title call depending on goroutine timing; the
// run's request is identified by its prompt instead.
func (m *promptCapturingModel) runRequest(t *testing.T, prompt string) fantasy.Prompt {
	t.Helper()
	m.mu.Lock()
	defer m.mu.Unlock()
	for i := len(m.requests) - 1; i >= 0; i-- {
		if strings.Contains(promptText(m.requests[i]), prompt) {
			return m.requests[i]
		}
	}
	require.Fail(t, "no captured request carried the turn prompt", prompt)
	return fantasy.Prompt{}
}

// TestRun_RecitesThisTurnsGoal is the regression for the staleness bug: Run
// snapshots the session before storing the new user message, so a recitation
// that read the last stored message would name the previous turn's request on
// every turn after the first.
func TestRun_RecitesThisTurnsGoal(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &promptCapturingModel{}
	model.text = "ok"
	sa := testSessionAgent(env, model, model, "system").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "goal")
	require.NoError(t, err)

	_, err = sa.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "FIRST REQUEST"})
	require.NoError(t, err)
	_, err = sa.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "SECOND REQUEST"})
	require.NoError(t, err)

	// The second request is what the model was working on, not the first.
	second := model.runRequest(t, "SECOND REQUEST")
	require.Contains(t, promptText(second), "Current goal: SECOND REQUEST")
	require.NotContains(t, promptText(second), "Current goal: FIRST REQUEST")
}

// TestRun_RecitesGoalNotTheSummaryAfterCompaction covers the same bug on the
// turn after a compaction, where the newest stored message is the summary
// that getSessionMessages re-roles to a user message.
func TestRun_RecitesGoalNotTheSummaryAfterCompaction(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &promptCapturingModel{}
	model.text = "## Current State\n- SUMMARY NARRATIVE GOES HERE"
	sa := testSessionAgent(env, model, model, "system").(*sessionAgent)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)
	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	sess, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, sess.SummaryMessageID)

	model.mu.Lock()
	model.text = "ok"
	model.mu.Unlock()

	_, err = sa.Run(ctx, SessionAgentCall{SessionID: sessionID, Prompt: "THIRD REQUEST"})
	require.NoError(t, err)

	third := model.runRequest(t, "THIRD REQUEST")
	require.Contains(t, promptText(third), "Current goal: THIRD REQUEST")
	require.NotContains(t, promptText(third), "Current goal: SUMMARY NARRATIVE")
}

// TestRun_HeadReminderCarriesTheStoredTodos proves the Run wiring, which the
// pure preparePrompt tests cannot: the list the model sees is the session's
// persisted one, not whatever a test passed in.
func TestRun_HeadReminderCarriesTheStoredTodos(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &promptCapturingModel{}
	model.text = "ok"
	sa := testSessionAgent(env, model, model, "system").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "stored todos")
	require.NoError(t, err)
	sess.Todos = []session.Todo{
		{Content: "Wire the reminder", Status: session.TodoStatusInProgress, ActiveForm: "Wiring the reminder"},
	}
	_, err = env.sessions.Save(ctx, sess)
	require.NoError(t, err)

	_, err = sa.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "carry on"})
	require.NoError(t, err)

	request := promptText(model.runRequest(t, "carry on"))
	require.Contains(t, request, "[in_progress] Wire the reminder")
	require.Contains(t, request, "Current task: Wiring the reminder")
}

// TestRun_SubAgentGetsNoRecitation keeps the parent's session state out of a
// sub-agent's prompt: it is not resuming the parent's work.
func TestRun_SubAgentGetsNoRecitation(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &promptCapturingModel{}
	model.text = "ok"
	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{Model: model, CatwalkCfg: catwalk.Model{ContextWindow: 200_000, DefaultMaxTokens: 10_000}},
		SmallModel: Model{Model: model, CatwalkCfg: catwalk.Model{ContextWindow: 200_000, DefaultMaxTokens: 10_000}},
		IsSubAgent: true,
		IsYolo:     true,
		Sessions:   env.sessions,
		Messages:   env.messages,
	}).(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "sub")
	require.NoError(t, err)

	_, err = sa.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "do the sub task"})
	require.NoError(t, err)

	request := promptText(model.runRequest(t, "do the sub task"))
	require.NotContains(t, request, "Current goal:")
	require.NotContains(t, request, "todo list is currently empty")
}

// TestPreparePrompt_SkipsWhitespaceOnlyAssistantTurn is the adjacency guard
// the tail recitation depends on: a stored assistant message whose text is
// only whitespace converts to an assistant turn with no parts, which would
// leave the recitation separated from the conversation by a blank turn that
// strict providers can reject.
func TestPreparePrompt_SkipsWhitespaceOnlyAssistantTurn(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "system").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "blank assistant")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "do the work"}},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.TextContent{Text: "   \n\t "}},
	})
	require.NoError(t, err)
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)

	history, _ := sa.preparePrompt(msgs, promptOptions{Recite: true, Goal: "do the work"}, true)

	for i, msg := range history {
		require.NotEmpty(t, msg.Content, "history[%d] (%s) must carry content", i, msg.Role)
	}
	require.Contains(t, lastText(t, history), "Current goal: do the work")
}
