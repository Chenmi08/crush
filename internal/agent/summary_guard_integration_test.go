package agent

import (
	"context"
	"errors"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// summaryStreamModel is a [fantasy.LanguageModel] stub that streams a fixed
// summary body and reports the configured finish reason, so the guard path in
// [sessionAgent.Summarize] can be exercised without a provider.
type summaryStreamModel struct {
	text         string
	finishReason fantasy.FinishReason
}

func (m *summaryStreamModel) Generate(context.Context, fantasy.Call) (*fantasy.Response, error) {
	return &fantasy.Response{
		Content:      fantasy.ResponseContent{fantasy.TextContent{Text: m.text}},
		FinishReason: m.finishReason,
	}, nil
}

func (m *summaryStreamModel) Stream(context.Context, fantasy.Call) (fantasy.StreamResponse, error) {
	text := m.text
	reason := m.finishReason
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
