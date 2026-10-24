package agent

import (
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// elisionHistory builds a session with one user message followed by n
// assistant/bash exchanges whose results are all large, so the last
// [elideKeepRecent] are the only ones a policy would keep.
func elisionHistory(t *testing.T, env fakeEnv, n int) (string, []message.Message) {
	t.Helper()
	ctx := t.Context()

	sess, err := env.sessions.Create(ctx, "elision")
	require.NoError(t, err)

	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "run the tests"}},
	})
	require.NoError(t, err)

	for i := 0; i < n; i++ {
		callID := "call_" + string(rune('a'+i))
		_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
			Role: message.Assistant,
			Parts: []message.ContentPart{
				message.ToolCall{
					ID:       callID,
					Name:     "bash",
					Input:    `{"command":"go test ./..."}`,
					Finished: true,
				},
			},
		})
		require.NoError(t, err)
		_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
			Role: message.Tool,
			Parts: []message.ContentPart{
				bashToolResult(callID, longOutput()),
			},
		})
		require.NoError(t, err)
	}

	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	return sess.ID, msgs
}

// toolResultTexts returns the body of every tool result in history, keyed by
// tool call id.
func toolResultTexts(t *testing.T, history []fantasy.Message) map[string]string {
	t.Helper()
	out := map[string]string{}
	for _, msg := range history {
		if msg.Role != fantasy.MessageRoleTool {
			continue
		}
		for _, part := range msg.Content {
			tr, ok := fantasy.AsMessagePart[fantasy.ToolResultPart](part)
			require.True(t, ok)
			out[tr.ToolCallID] = toolResultOutputText(tr.Output)
		}
	}
	return out
}

// Off mode is the regression guard: every payload reaches the model exactly
// as it was stored, in the same position, with nothing added.
func TestPreparePrompt_ElideOffKeepsEveryPayload(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)
	_, msgs := elisionHistory(t, env, 6)

	stored := map[string]string{}
	for _, m := range msgs {
		for _, r := range m.ToolResults() {
			stored[r.ToolCallID] = r.Content
		}
	}

	policy := defaultElidePolicy()
	policy.Mode = elideOff
	history, _ := sa.preparePrompt(msgs, promptOptions{Elide: policy}, false)

	require.Equal(t, stored, toolResultTexts(t, history), "off mode must send the stored payloads verbatim")
}

func TestPreparePrompt_ElideShadowKeepsFullContent(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)
	_, msgs := elisionHistory(t, env, 6)

	// Shadow is the default, so this is what ships.
	history, _ := sa.preparePrompt(msgs, promptOptions{Elide: defaultElidePolicy()}, false)

	results := toolResultTexts(t, history)
	require.Len(t, results, 6)
	for id, body := range results {
		require.NotContains(t, body, "[elided]", id)
		require.Contains(t, body, "--- FAIL: TestSummarize", id)
	}
}

func TestPreparePrompt_ElideOnReplacesOldPayloads(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)
	_, msgs := elisionHistory(t, env, 6)

	policy := defaultElidePolicy()
	policy.Mode = elideOn
	history, _ := sa.preparePrompt(msgs, promptOptions{Elide: policy}, false)

	results := toolResultTexts(t, history)
	require.Len(t, results, 6)

	// The three oldest are elided, the three newest are not: KeepRecent is
	// counted over the flattened result order.
	for _, id := range []string{"call_a", "call_b", "call_c"} {
		require.Contains(t, results[id], "[elided]", id)
		require.Contains(t, results[id], `recall(tool_call_id="`+id+`")`, id)
		require.NotContains(t, results[id], "some ordinary log line", id)
	}
	for _, id := range []string{"call_d", "call_e", "call_f"} {
		require.NotContains(t, results[id], "[elided]", id)
		require.Contains(t, results[id], "some ordinary log line", id)
	}

	// Elision changes the content, never the position: adjacency holds.
	requireToolCallAdjacency(t, history)
}

func TestPreparePrompt_ElideKeepsTurnLocalResults(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)
	_, msgs := elisionHistory(t, env, 6)

	policy := defaultElidePolicy()
	policy.Mode = elideOn
	history, _ := sa.preparePrompt(msgs, promptOptions{
		Elide:       policy,
		ExemptElide: map[string]struct{}{"call_a": {}},
	}, false)

	results := toolResultTexts(t, history)
	require.NotContains(t, results["call_a"], "[elided]",
		"a result produced during this turn must survive it")
	require.Contains(t, results["call_b"], "[elided]")
}

func TestElideExemptTravelsThroughContext(t *testing.T) {
	t.Parallel()

	ctx := t.Context()
	require.Nil(t, elideExemptFrom(ctx))

	ids := map[string]struct{}{"call_x": {}}
	require.Equal(t, ids, elideExemptFrom(withElideExempt(ctx, ids)))

	// An empty set is not attached: there is nothing to exempt, and the
	// nil result is what the prompt builder treats as "no exemption".
	require.Nil(t, elideExemptFrom(withElideExempt(ctx, nil)))
}

func TestSummarize_ElidesOldToolResultsInPrompt(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{text: longSummaryBody(), finishReason: fantasy.FinishReasonStop}
	sa := summaryTestAgent(env, model)
	sa.elidePolicy = elidePolicy{Mode: elideOn, KeepRecent: 1, KeepBytes: 1}

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	// One old failing result, plus the recent one the policy keeps.
	_, err := env.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role: message.Assistant,
		Parts: []message.ContentPart{
			message.ToolCall{ID: "call_old", Name: "bash", Input: `{"command":"go test ./..."}`, Finished: true},
			message.ToolCall{ID: "call_new", Name: "bash", Input: `{"command":"go build ./..."}`, Finished: true},
		},
	})
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sessionID, message.CreateMessageParams{
		Role: message.Tool,
		Parts: []message.ContentPart{
			bashToolResult("call_old", longOutput()),
			bashToolResult("call_new", longOutput()),
		},
	})
	require.NoError(t, err)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	// The summarizer's request is built from the same prompt path, so the
	// tombstone has to be visible there rather than only in a turn prompt.
	var sawElided bool
	for _, prompt := range model.recordedHistories() {
		if strings.Contains(prompt, "[elided] tool_call_id=call_old") {
			sawElided = true
		}
	}
	require.True(t, sawElided, "the compaction prompt should carry tombstones")
}

// Shadow mode exists to measure without changing behavior, so the rendered
// history has to be byte-identical to the pre-PR one.
func TestPreparePrompt_ElideShadowMatchesOff(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)
	_, msgs := elisionHistory(t, env, 6)

	off := defaultElidePolicy()
	off.Mode = elideOff
	offHistory, _ := sa.preparePrompt(msgs, promptOptions{Elide: off}, false)

	shadowHistory, _ := sa.preparePrompt(msgs, promptOptions{Elide: defaultElidePolicy()}, false)

	require.Equal(t, flattenHistory(offHistory), flattenHistory(shadowHistory))
}

// flattenHistory renders a history deterministically for equality checks.
func flattenHistory(history []fantasy.Message) string {
	var sb strings.Builder
	for _, msg := range history {
		sb.WriteString(string(msg.Role))
		sb.WriteString(":")
		for _, part := range msg.Content {
			switch p := part.(type) {
			case fantasy.TextPart:
				sb.WriteString(p.Text)
			case fantasy.ToolCallPart:
				sb.WriteString(p.ToolCallID + p.ToolName + p.Input)
			case fantasy.ToolResultPart:
				sb.WriteString(p.ToolCallID)
				sb.WriteString(toolResultOutputText(p.Output))
			}
			sb.WriteString("\x00")
		}
		sb.WriteString("\n")
	}
	return sb.String()
}

// The tombstone must round-trip: the handle it advertises has to resolve to
// the payload it replaced, or the "reversible" claim is false.
func TestRenderTombstone_HandleResolvesThroughRecall(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)
	sessionID, msgs := elisionHistory(t, env, 6)

	policy := defaultElidePolicy()
	policy.Mode = elideOn
	history, _ := sa.preparePrompt(msgs, promptOptions{Elide: policy}, false)

	results := toolResultTexts(t, history)
	elided := results["call_a"]
	require.Contains(t, elided, "[elided]")
	require.Contains(t, elided, `recall(tool_call_id="call_a")`)

	// The stored row still holds the full payload.
	stored, err := env.messages.ListToolMessages(t.Context(), sessionID)
	require.NoError(t, err)
	var found bool
	for _, msg := range stored {
		for _, r := range msg.ToolResults() {
			if r.ToolCallID != "call_a" {
				continue
			}
			found = true
			require.Equal(t, longOutput(), r.Content, "elision must not touch the store")
		}
	}
	require.True(t, found)
}

// Elision is a display transform: it must not write to the message store.
func TestPreparePrompt_ElideOnDoesNotPersist(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa := testSessionAgent(env, nil, nil, "test prompt").(*sessionAgent)
	sessionID, msgs := elisionHistory(t, env, 6)

	policy := defaultElidePolicy()
	policy.Mode = elideOn
	_, _ = sa.preparePrompt(msgs, promptOptions{Elide: policy}, false)

	require.NoError(t, env.messages.FlushAll(t.Context()))
	stored, err := env.messages.List(t.Context(), sessionID)
	require.NoError(t, err)
	for _, msg := range stored {
		for _, r := range msg.ToolResults() {
			require.NotContains(t, r.Content, "[elided]", "the store must keep the original payload")
		}
	}
}
