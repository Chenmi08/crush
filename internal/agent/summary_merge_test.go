package agent

import (
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestSplitPreviousSummary(t *testing.T) {
	t.Parallel()

	narrative := "## Current State\n- work is half done"

	tests := []struct {
		name    string
		content string
		prefix  int
		want    string
	}{
		{
			name:    "no appended blocks",
			content: narrative,
			want:    narrative,
		},
		{
			name:    "legacy manifest appended",
			content: narrative + "\n\n" + legacyManifestLeadIn + "<context_manifest></context_manifest>",
			want:    narrative,
		},
		{
			name: "appendix appended",
			content: narrative + "\n\n" + appendixLeadIn + "\n\n" +
				manifestLeadIn + "<context_manifest></context_manifest>",
			want: narrative,
		},
		{
			name:    "legacy manifest and transcript appended",
			content: narrative + "\n\n" + legacyManifestLeadIn + "<context_manifest></context_manifest>" + "\n\n## Full Transcript\n\nstored at /tmp/x",
			want:    narrative,
		},
		{
			name: "prefix length wins",
			// The stored length is authoritative, even when the lead-in
			// text appears inside the narrative itself: a summary that
			// quotes the lead-in must not be cut there.
			content: narrative + "\nThe manifest lead-in reads: " + manifestLeadIn,
			prefix:  len(narrative),
			want:    narrative,
		},
		{
			name:    "prefix length past the end falls back",
			content: narrative,
			prefix:  len(narrative) + 100,
			want:    narrative,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			got := splitPreviousSummary(message.Message{
				Parts: []message.ContentPart{message.TextContent{Text: tt.content}},
			}, tt.prefix)
			require.Equal(t, tt.want, got)
		})
	}
}

func TestNarrativeCut(t *testing.T) {
	t.Parallel()

	narrative := "## Current State\n- work is half done"
	require.Equal(t, len(narrative), narrativeCut(narrative), "no appended block cuts at the end")

	// The appendix marker is the single boundary for summaries written now.
	withAppendix := narrative + "\n\n" + appendixLeadIn + "\n\n" + manifestLeadIn + "<context_manifest/>"
	require.Equal(t, len(narrative)+2, narrativeCut(withAppendix))

	// The legacy per-block markers still cut stored summaries written before
	// the appendix existed. The old manifest lead-in is a pinned literal: the
	// current one is shorter, so only the old wording can find a stored block.
	withManifest := narrative + "\n\n" + legacyManifestLeadIn + "<context_manifest/>"
	require.Equal(t, len(narrative)+2, narrativeCut(withManifest))

	// The current lead-in only ever appears inside the appendix, so on its own
	// it is prose, not a boundary.
	currentOnly := narrative + "\n\n" + manifestLeadIn + "<context_manifest/>"
	require.Equal(t, len(currentOnly), narrativeCut(currentOnly))

	// A transcript-only summary still has a boundary: the manifest is
	// omitted when there are no failures and no changed files.
	withTranscript := narrative + "\n\n" + transcriptBlockLeadIn + "\n\nstored"
	require.Equal(t, len(narrative)+2, narrativeCut(withTranscript))

	// The earliest marker wins regardless of the order they appear in.
	both := narrative + "\n\n" + transcriptBlockLeadIn + "\n\nstored\n\n" + legacyManifestLeadIn + "<x/>"
	require.Equal(t, len(narrative)+2, narrativeCut(both))
}

func TestNarrativeCutIgnoresQuotedMarkers(t *testing.T) {
	t.Parallel()

	narrative := "## Current State\n- work is half done"

	// Prose can quote a marker: a summary of the code that defines these
	// constants does. Quoted inline it is not a boundary.
	inline := narrative + "\n\nleadIn is `" + appendixLeadIn + "` in summary_appendix.go"
	require.Equal(t, len(inline), narrativeCut(inline))

	// The appendix is always the final block, so a line-start quote inside
	// the prose (a fenced sample) does not move the boundary either: the
	// last line-start occurrence wins.
	fenced := narrative + "\n\n```\n" + appendixLeadIn + "\n```\n\nmore prose"
	withQuote := fenced + "\n\n" + appendixLeadIn + "\n\n" + manifestLeadIn + "<context_manifest/>"
	require.Equal(t, len(fenced)+2, narrativeCut(withQuote))

	// A legacy marker quoted inline is not a boundary either.
	inlineLegacy := narrative + "\n\n- dropped `" + recentlyModifiedBlockLeadIn + "` from summary_recent.go"
	require.Equal(t, len(inlineLegacy), narrativeCut(inlineLegacy))
}

func TestBuildSummaryPrompt_MergeInstructionOnlyWithPrevious(t *testing.T) {
	t.Parallel()

	todos := []session.Todo{{Content: "finish the guard", Status: session.TodoStatusInProgress}}

	first := buildSummaryPrompt(todos, "")
	require.NotContains(t, first, "Merging a previous summary")

	previous := "## Current State\n- half done " + strings.Repeat("with detail ", 40)
	merged := buildSummaryPrompt(todos, previous)
	require.Contains(t, merged, mergeInstruction(previous))
	require.Contains(t, merged, "Merging a previous summary")
	require.Contains(t, merged, "tokens", "the size floor has to name a number to be actionable")
	require.Contains(t, merged, "superseded by")

	// The todo block survives the merge instruction; the summary still has
	// to restate the list.
	require.Contains(t, merged, "finish the guard")
	require.Contains(t, merged, "Restate this list")

	// An empty todo list still gets its notice, and still gets the merge.
	empty := buildSummaryPrompt(nil, "previous")
	require.Contains(t, empty, "no todo list")
	require.Contains(t, empty, "Merging a previous summary")
}

func TestSummarize_FirstCompactionHasNoMergeInstruction(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{text: longSummaryBody(), finishReason: fantasy.FinishReasonStop}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))
	require.NotContains(t, model.recordedPrompts()[0], "Merging a previous summary")
}

func TestSummarize_SecondCompactionMergesPreviousNarrative(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{bodies: []string{
		longSummaryBody(),
		longSummaryBody() + "\n## Exact Next Steps\n1. keep going\n",
	}}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))
	first, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	firstMessage, err := env.messages.Get(ctx, first.SummaryMessageID)
	require.NoError(t, err)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	prompts := model.recordedPrompts()
	second := prompts[len(prompts)-1]
	require.Contains(t, second, "Merging a previous summary", "the second compaction must be a merge")

	// The merge instruction is paired with the previous narrative being
	// present in the model's history, not with a re-serialized copy in the
	// prompt: getSessionMessages already returns it as the first message.
	// The narrative itself is already in the request as the first message;
	// the prompt adds the instruction, not a second copy of the document.
	histories := model.recordedHistories()
	require.Contains(t, histories[len(histories)-1], "now rejects empty summaries")

	// The stored prefix must slice the new summary back to its narrative.
	secondSession, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.Greater(t, secondSession.SummaryPromptPrefixLen, 0)
	secondMessage, err := env.messages.Get(ctx, secondSession.SummaryMessageID)
	require.NoError(t, err)
	content := secondMessage.Content().Text
	require.LessOrEqual(t, secondSession.SummaryPromptPrefixLen, len(content))
	require.NotContains(t, content[:secondSession.SummaryPromptPrefixLen], manifestLeadIn)
	require.NotContains(t, content[:secondSession.SummaryPromptPrefixLen], "## Full Transcript")

	// The anchor names the oldest message the summary replaces: the
	// previous summary itself, which is the first element of the span.
	require.Equal(t, firstMessage.ID, secondSession.SummaryAnchorMessageID)
}

// The collapse guard still rejects a merge that loses most of the previous
// summary, even though the span it replaces is small.
func TestSummarize_CollapsedMergeStillRejected(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{bodies: []string{
		longSummaryBody(),
		"## Current State\nnone\n",
		"## Current State\nnone\n",
	}}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))
	first, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)

	err = sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil)
	require.ErrorIs(t, err, errSummaryCollapsed)

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, first.SummaryMessageID, after.SummaryMessageID)
	require.Equal(t, first.SummaryAnchorMessageID, after.SummaryAnchorMessageID)
}

func TestSummarize_AnchorIsOldestArchivedMessage(t *testing.T) {
	t.Parallel()

	env := testEnv(t)
	model := &summaryStreamModel{text: longSummaryBody(), finishReason: fantasy.FinishReasonStop}
	sa := summaryTestAgent(env, model)

	ctx := t.Context()
	sessionID := summaryTestSession(t, env)

	msgs, err := env.messages.List(ctx, sessionID)
	require.NoError(t, err)
	require.NotEmpty(t, msgs)

	require.NoError(t, sa.Summarize(ctx, sessionID, fantasy.ProviderOptions{}, nil))

	after, err := env.sessions.Get(ctx, sessionID)
	require.NoError(t, err)
	require.Equal(t, msgs[0].ID, after.SummaryAnchorMessageID)
}
