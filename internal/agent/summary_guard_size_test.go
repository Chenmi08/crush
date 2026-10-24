package agent

import (
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// longSummaryBody is a plausible complete summary. Each line is a fact the
// collapse guard is meant to protect.
func longSummaryBody() string {
	return strings.Repeat("## Current State\n- `internal/agent/agent.go:1532` now rejects empty summaries.\n", 120)
}

func TestSummarize_CollapsedSummaryKeepsPreviousAnchor(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{bodies: []string{longSummaryBody(), "## Current State\nnone\n"}}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))
	first, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, first.SummaryMessageID)

	err = sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil)
	require.ErrorIs(t, err, errSummaryCollapsed)

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, first.SummaryMessageID, after.SummaryMessageID,
		"a collapsed summary must not become the session's only context")

	// One attempt plus the single retry, not a loop.
	require.Equal(t, 3, model.callCount())
	prompts := model.recordedPrompts()
	require.Contains(t, prompts[2], "Retry: the previous attempt collapsed")
	require.NotContains(t, prompts[1], "Retry: the previous attempt collapsed")

	// Both rejected attempts are visible as failed messages, and none of
	// them outlive a later prompt as a second version of the session state.
	require.NoError(t, env.messages.FlushAll(ctx))
	msgs, err := env.messages.List(ctx, sessionID)
	require.NoError(t, err)
	failed := 0
	for _, msg := range msgs {
		if msg.IsSummaryMessage && msg.FinishReason() == message.FinishReasonError {
			failed++
			require.Contains(t, msg.FinishPart().Details, "less than half")
		}
	}
	require.Equal(t, 2, failed)

	history, err := sa.getSessionMessages(ctx, after)
	require.NoError(t, err)
	for _, msg := range history {
		if msg.IsSummaryMessage {
			require.Equal(t, first.SummaryMessageID, msg.ID,
				"only the authoritative summary belongs in the prompt")
		}
	}
}

func TestSummarize_CollapseRetrySucceeds(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{bodies: []string{
		longSummaryBody(),
		"## Current State\nnone\n",
		longSummaryBody() + "\n## Exact Next Steps\n1. run the tests\n",
	}}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))
	first, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotEqual(t, first.SummaryMessageID, after.SummaryMessageID)
	require.Equal(t, 3, model.callCount())

	msg, err := env.messages.Get(ctx, after.SummaryMessageID)
	require.NoError(t, err)
	require.Equal(t, message.FinishReasonEndTurn, msg.FinishReason())
	require.Contains(t, msg.Content().Text, "## Exact Next Steps")
}

func TestSummarize_InflatedSummaryIsRejectedWithoutRetry(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	// The first body is short enough to fit in the context it replaces; the
	// second is longer than the whole context, so it would grow the session.
	inflating := strings.Repeat("## User Messages\n- a very long restatement\n", 200)
	model := &summaryStreamModel{bodies: []string{
		"## Current State\n- small\n",
		inflating,
	}}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))
	first, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, first.SummaryMessageID)

	err = sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil)
	require.ErrorIs(t, err, errSummaryInflated)

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, first.SummaryMessageID, after.SummaryMessageID)
	require.Equal(t, 2, model.callCount(), "inflation is rejected without a retry")
}

func TestSummarize_FirstCompactionSkipsSizeGuards(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	// Tiny body, no previous summary and no counters: both size guards are
	// inert, so the compaction succeeds.
	model := &summaryStreamModel{bodies: []string{"## Current State\nnone\n"}}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, after.SummaryMessageID)
	require.Equal(t, 1, model.callCount())
}

// A session that already lost the previous summary row must not be blocked by
// the guards: there is nothing to compare against, so the compaction proceeds
// with whatever context the session still has.
func TestSummarize_MissingPreviousSummaryRowStillCompacts(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{bodies: []string{"## Current State\nnone\n"}}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "dangling anchor")
	require.NoError(t, err)
	_, err = env.messages.Create(ctx, sess.ID, message.CreateMessageParams{
		Role:  message.User,
		Parts: []message.ContentPart{message.TextContent{Text: "keep going"}},
	})
	require.NoError(t, err)

	sess, err = env.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	sess.SummaryMessageID = "does-not-exist"
	_, err = env.sessions.Save(ctx, sess)
	require.NoError(t, err)

	require.NoError(t, sa.Summarize(ctx, sess.ID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.NotEqual(t, "does-not-exist", after.SummaryMessageID)
}
