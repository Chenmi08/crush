package agent

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

// summaryTemplateSections are the seven headings the model authors (PRD §4.7
// items 1-7). "## Full Transcript" is injected by code at persistence time
// and must never appear in the template: the model would guess the path.
var summaryTemplateSections = []string{
	"## Current State",
	"## User Messages",
	"## Constraints",
	"## Decisions",
	"## Rejected Approaches",
	"## Artifacts",
	"## Exact Next Steps",
}

func TestSummaryTemplateContainsRequiredSections(t *testing.T) {
	t.Parallel()

	template := string(summaryPrompt)

	last := -1
	for _, heading := range summaryTemplateSections {
		idx := strings.Index(template, heading)
		require.GreaterOrEqual(t, idx, 0, "template is missing heading %q", heading)
		require.Greater(t, idx, last, "heading %q is out of order", heading)
		last = idx
	}

	require.NotContains(t, template, "## Full Transcript",
		"the transcript section is injected by code, not authored by the model")
	require.NotContains(t, template, "## Files & Changes",
		"the old template's sections were replaced wholesale")
}

func TestSummaryTemplatePreamble(t *testing.T) {
	t.Parallel()

	template := string(summaryPrompt)

	// The opening line must say the summary is all that survives.
	require.Contains(t, strings.ToLower(template), "only context")
	require.Contains(t, strings.ToLower(template), "gone")
	require.Less(t, strings.Index(template, "only context"), 200,
		"the survival statement belongs in the opening line, not buried below the preamble")

	// Tool use must be forbidden, explicitly and with a reason.
	require.Contains(t, strings.ToLower(template), "tool use is forbidden")
	require.Contains(t, strings.ToLower(template), "tool calls will be rejected")

	// The scratchpad is permitted and stripped by code.
	require.Contains(t, template, "<analysis>")

	// Length is uncapped but no section may be skipped.
	require.NotContains(t, template, "**Length**: No limit")
	require.Contains(t, strings.ToLower(template), "every section is mandatory")
	require.Contains(t, template, "`none`")

	// User messages must be verbatim and complete.
	require.Contains(t, template, "verbatim")
}

func TestBuildSummaryPromptWithTodos(t *testing.T) {
	t.Parallel()

	todos := []session.Todo{
		{Content: "Guard truncated summaries", Status: session.TodoStatusCompleted},
		{Content: "Rewrite the summary section", Status: session.TodoStatusInProgress, ActiveForm: "Rewriting the summary section"},
		{Content: "Add manifest", Status: session.TodoStatusPending},
	}

	prompt := buildSummaryPrompt(todos)

	for _, todo := range todos {
		require.Contains(t, prompt, todo.Content)
		require.Contains(t, prompt, string(todo.Status))
	}
	require.Contains(t, prompt, "Rewriting the summary section")

	// The injected block is the prompt; the output must restate it.
	require.Contains(t, prompt, "## Current State")
	require.Contains(t, prompt, "Restate this list")

	// An empty-session notice must not appear when todos exist.
	require.NotContains(t, prompt, "no todo list")
}

func TestBuildSummaryPromptWithoutTodos(t *testing.T) {
	t.Parallel()

	prompt := buildSummaryPrompt(nil)

	require.Contains(t, prompt, "no todo list")
	require.Contains(t, prompt, "do not invent one")
	require.Contains(t, prompt, "## Current State")
	require.NotContains(t, prompt, "Restate this list")
}

func TestStripAnalysisBlock(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "leading block removed",
			in:   "<analysis>\nthinking hard\n</analysis>\n\n## Current State\n- done",
			want: "## Current State\n- done",
		},
		{
			name: "leading block with surrounding whitespace",
			in:   "\n\n  <analysis>thinking</analysis>\n\n## Current State\n- done",
			want: "## Current State\n- done",
		},
		{
			name: "case insensitive tags",
			in:   "<ANALYSIS>thinking</Analysis>\n## Current State\n- done",
			want: "## Current State\n- done",
		},
		{
			name: "absent block untouched",
			in:   "## Current State\n- done",
			want: "## Current State\n- done",
		},
		{
			name: "inline mention not stripped",
			in:   "## Current State\n- the model wrote <analysis> tags",
			want: "## Current State\n- the model wrote <analysis> tags",
		},
		{
			name: "only the first block removed",
			in:   "<analysis>one</analysis>\n## Decisions\n- kept <analysis>two</analysis>",
			want: "## Decisions\n- kept <analysis>two</analysis>",
		},
		{
			name: "unterminated block left alone",
			in:   "<analysis>no end",
			want: "<analysis>no end",
		},
		{
			name: "empty string",
			in:   "",
			want: "",
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, stripAnalysisBlock(tt.in))
		})
	}
}
