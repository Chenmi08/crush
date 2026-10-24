package agent

import (
	"context"
	"errors"
	"testing"
	"time"

	"charm.land/catwalk/pkg/catwalk"
	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/notify"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/pubsub"
	"github.com/stretchr/testify/require"
)

// summaryNoticeAgent builds a summarization agent whose notifications can be
// observed.
func summaryNoticeAgent(t *testing.T, env fakeEnv, model fantasy.LanguageModel) (*sessionAgent, <-chan pubsub.Event[notify.Notification]) {
	t.Helper()

	broker := pubsub.NewBroker[notify.Notification]()
	t.Cleanup(broker.Shutdown)

	sa := NewSessionAgent(SessionAgentOptions{
		LargeModel: Model{Model: model},
		SmallModel: Model{Model: model},
		IsYolo:     true,
		Sessions:   env.sessions,
		Messages:   env.messages,
		Notify:     broker,
	}).(*sessionAgent)
	return sa, broker.Subscribe(t.Context())
}

// drainNotifications collects every notification already published, failing
// when none arrives within a short window.
func drainNotifications(t *testing.T, ch <-chan pubsub.Event[notify.Notification]) []notify.Notification {
	t.Helper()

	var got []notify.Notification
	for {
		select {
		case ev := <-ch:
			got = append(got, ev.Payload)
		case <-time.After(50 * time.Millisecond):
			return got
		}
	}
}

// TestSummarize_CircuitBreakerStopsAfterConsecutiveFailures is the point of
// PR-3: a session that fails to summarize three times in a row must stop
// paying for a full-context request on every subsequent turn.
func TestSummarize_CircuitBreakerStopsAfterConsecutiveFailures(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{
		text:         "## Current State\n- partial",
		finishReason: fantasy.FinishReasonLength,
	}
	sa, _ := summaryNoticeAgent(t, env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	for i := 1; i <= maxSummaryFailures; i++ {
		err := sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil)
		require.ErrorIs(t, err, errSummaryTruncated, "attempt %d", i)

		sess, getErr := env.sessions.Get(ctx, sessionID)
		require.NoError(t, getErr)
		require.Equal(t, i, sess.SummaryFailCount, "each rejection must be counted")
		require.Empty(t, sess.SummaryMessageID, "a rejected summary must not become the anchor")
	}
	require.Equal(t, maxSummaryFailures, model.callCount())

	// The breaker is open: the next attempt returns without touching the
	// provider at all.
	err := sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil)
	require.ErrorIs(t, err, errSummaryDisabled)
	require.Equal(t, maxSummaryFailures, model.callCount(),
		"an open breaker must not issue a provider request")
}

// TestSummarize_SuccessResetsFailureCount makes the counter consecutive, not
// cumulative: one good summary buys another three attempts.
func TestSummarize_SuccessResetsFailureCount(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{
		text:         "## Current State\n- done",
		finishReason: fantasy.FinishReasonStop,
		reasons: []fantasy.FinishReason{
			fantasy.FinishReasonLength,
			fantasy.FinishReasonLength,
			fantasy.FinishReasonStop,
		},
	}
	sa, _ := summaryNoticeAgent(t, env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.ErrorIs(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil), errSummaryTruncated)
	require.ErrorIs(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil), errSummaryTruncated)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	sess, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.Zero(t, sess.SummaryFailCount)
	require.NotEmpty(t, sess.SummaryMessageID)
}

// TestSummarize_BreakerTripPublishesOneNotice pins the "exactly one notice"
// requirement: the trip that crosses the threshold announces itself, and the
// attempts after it do not announce anything again because they never reach
// the provider.
func TestSummarize_BreakerTripPublishesOneNotice(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{
		text:         "## Current State\n- partial",
		finishReason: fantasy.FinishReasonLength,
	}
	sa, notes := summaryNoticeAgent(t, env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	for i := 0; i < maxSummaryFailures+2; i++ {
		_ = sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil)
	}

	var disabled []notify.Notification
	for _, n := range drainNotifications(t, notes) {
		if n.Type == notify.TypeSummarizeDisabled {
			disabled = append(disabled, n)
		}
	}
	require.Len(t, disabled, 1, "only the trip that crossed the threshold notifies")
	require.Equal(t, sessionID, disabled[0].SessionID)
	require.Contains(t, disabled[0].Message, "paused")
}

// TestSummarize_ProviderErrorCountsTowardBreaker covers the failure shape the
// guards do not see: a stream that errors out is still a failed compaction,
// so a provider that keeps failing must trip the breaker too.
func TestSummarize_ProviderErrorCountsTowardBreaker(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	sa, _ := summaryNoticeAgent(t, env, &errorStreamModel{})

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	for i := 1; i <= maxSummaryFailures; i++ {
		require.Error(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))
		sess, err := env.sessions.Get(ctx, sessionID)
		require.NoError(t, err)
		require.Equal(t, i, sess.SummaryFailCount)
	}

	require.ErrorIs(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil), errSummaryDisabled)
}

// errorStreamModel fails every stream, so the provider-error path can be
// exercised without a cassette.
type errorStreamModel struct{}

func (m *errorStreamModel) Provider() string { return "fake" }
func (m *errorStreamModel) Model() string    { return "fake-model" }

func (m *errorStreamModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return nil, errors.New("provider exploded")
}

func (m *errorStreamModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	return nil, errors.New("provider exploded")
}

func (m *errorStreamModel) GenerateObject(context.Context, fantasy.ObjectCall) (*fantasy.ObjectResponse, error) {
	return nil, errors.New("not implemented")
}

func (m *errorStreamModel) StreamObject(context.Context, fantasy.ObjectCall) (fantasy.ObjectStreamResponse, error) {
	return nil, errors.New("not implemented")
}

// TestRun_TrippedBreakerDoesNotCutTheTurnShort covers the Run-side contract:
// StopWhen consults the breaker, so a session whose breaker is open is not
// stopped for compaction at all. That is what makes "skip summarization and
// continue the turn" (D11) true without re-queuing the call forever.
func TestRun_TrippedBreakerDoesNotCutTheTurnShort(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &finishStreamModel{text: "done"}
	sa := testSessionAgent(env, model, model, "system").(*sessionAgent)

	ctx := t.Context()
	sess, err := env.sessions.Create(ctx, "tripped")
	require.NoError(t, err)

	// A context window so small that any usage is over the threshold,
	// plus an open breaker.
	sess.PromptTokens = 95
	sess.SummaryFailCount = maxSummaryFailures
	sess, err = env.sessions.Save(ctx, sess)
	require.NoError(t, err)

	small := Model{
		Model:      model,
		CatwalkCfg: catwalk.Model{ContextWindow: 100, DefaultMaxTokens: 10},
	}
	sa.SetModels(small, small)

	result, err := sa.Run(ctx, SessionAgentCall{SessionID: sess.ID, Prompt: "carry on"})
	require.NoError(t, err)
	require.NotNil(t, result)

	after, err := env.sessions.Get(ctx, sess.ID)
	require.NoError(t, err)
	require.Empty(t, after.SummaryMessageID, "a tripped breaker must not compact")
	require.Equal(t, maxSummaryFailures, after.SummaryFailCount)

	require.NoError(t, env.messages.FlushAll(ctx))
	msgs, err := env.messages.List(ctx, sess.ID)
	require.NoError(t, err)
	require.Len(t, msgs, 2)
	require.Equal(t, message.Assistant, msgs[1].Role)
	require.Equal(t, message.FinishReasonEndTurn, msgs[1].FinishReason())
}
