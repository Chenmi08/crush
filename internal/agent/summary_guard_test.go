package agent

import (
	"fmt"
	"strings"
	"testing"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/stretchr/testify/require"
)

func TestSummaryOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name    string
		resp    *fantasy.AgentResult
		wantErr error
	}{
		{
			name: "nil result is not a truncation",
			resp: nil,
		},
		{
			name: "stop is complete",
			resp: &fantasy.AgentResult{
				Response: fantasy.Response{FinishReason: fantasy.FinishReasonStop},
			},
		},
		{
			name: "length is truncation",
			resp: &fantasy.AgentResult{
				Response: fantasy.Response{FinishReason: fantasy.FinishReasonLength},
			},
			wantErr: errSummaryTruncated,
		},
		{
			name: "empty finish reason is tolerated",
			resp: &fantasy.AgentResult{Response: fantasy.Response{}},
		},
		{
			name: "unknown finish reason is tolerated",
			resp: &fantasy.AgentResult{
				Response: fantasy.Response{FinishReason: fantasy.FinishReasonUnknown},
			},
		},
		{
			name: "tool calls are not truncation",
			resp: &fantasy.AgentResult{
				Response: fantasy.Response{FinishReason: fantasy.FinishReasonToolCalls},
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := summaryOutcome(tt.resp)
			if tt.wantErr != nil {
				require.ErrorIs(t, err, tt.wantErr)
				require.ErrorIs(t, err, errSummaryTruncated)
				require.Contains(t, err.Error(), "truncated")
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestValidateSummary(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name string
		s    summarySizes
		want error
	}{
		{
			name: "first compaction has no collapse baseline",
			s:    summarySizes{PrevTokens: 0, NewTokens: 1, PrevContext: 0},
		},
		{
			name: "equal sizes are allowed",
			s:    summarySizes{PrevTokens: 100, NewTokens: 100, PrevContext: 1000},
		},
		{
			name: "growing is allowed",
			s:    summarySizes{PrevTokens: 100, NewTokens: 180, PrevContext: 1000},
		},
		{
			name: "sixty percent is allowed",
			s:    summarySizes{PrevTokens: 100, NewTokens: 60, PrevContext: 1000},
		},
		{
			name: "exactly half is allowed",
			s:    summarySizes{PrevTokens: 100, NewTokens: 50, PrevContext: 1000},
		},
		{
			name: "forty percent is collapsed",
			s:    summarySizes{PrevTokens: 100, NewTokens: 40, PrevContext: 1000},
			want: errSummaryCollapsed,
		},
		{
			name: "a summary larger than the replaced context is inflated",
			s:    summarySizes{PrevTokens: 100, NewTokens: 1000, PrevContext: 1000},
			want: errSummaryInflated,
		},
		{
			name: "inflation needs a non-zero context",
			s:    summarySizes{PrevTokens: 0, NewTokens: 5000, PrevContext: 0},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			err := validateSummary(tt.s)
			if tt.want != nil {
				require.ErrorIs(t, err, tt.want)
				return
			}
			require.NoError(t, err)
		})
	}
}

func TestSummaryNarrativeTokens(t *testing.T) {
	t.Parallel()

	t.Run("no manifest", func(t *testing.T) {
		t.Parallel()

		body := strings.Repeat("fact ", 40)
		require.Equal(t, approxTokenCount(strings.TrimSpace(body)), summaryNarrativeTokens(body))
	})

	t.Run("manifest is excluded", func(t *testing.T) {
		t.Parallel()

		body := strings.Repeat("fact ", 40)
		content := body + "\n\n" + manifestLeadIn + "<context_manifest generated=\"x\">\narchived: messages=1\n</context_manifest>"
		require.Equal(t, approxTokenCount(strings.TrimSpace(body)), summaryNarrativeTokens(content))
	})

	t.Run("recently modified list is excluded", func(t *testing.T) {
		t.Parallel()

		// The list is the only appended block a compaction with no
		// failures and no changed files produces, so it is the case a
		// manifest-only boundary would miscount.
		body := strings.Repeat("fact ", 40)
		content := body + "\n\n" + recentlyModifiedBlock([]string{"/ws/a.go"}, "/ws")
		require.Equal(t, approxTokenCount(strings.TrimSpace(body)), summaryNarrativeTokens(content))
	})

	t.Run("empty", func(t *testing.T) {
		t.Parallel()

		require.Zero(t, summaryNarrativeTokens(""))
	})
}

func TestSummarizeOutcome(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name      string
		err       error
		wantSkip  bool
		wantFatal error
	}{
		{
			name: "success is neither skipped nor fatal",
		},
		{
			name:     "tripped breaker is a skip",
			err:      errSummaryDisabled,
			wantSkip: true,
		},
		{
			name:      "truncation is fatal",
			err:       errSummaryTruncated,
			wantFatal: errSummaryTruncated,
		},
		{
			name:      "collapse is fatal",
			err:       errSummaryCollapsed,
			wantFatal: errSummaryCollapsed,
		},
		{
			name:      "inflation is fatal",
			err:       errSummaryInflated,
			wantFatal: errSummaryInflated,
		},
		{
			name:      "wrapped breaker error is still a skip",
			err:       fmt.Errorf("summarize: %w", errSummaryDisabled),
			wantSkip:  true,
			wantFatal: nil,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			skip, fatal := summarizeOutcome(tt.err)
			require.Equal(t, tt.wantSkip, skip)
			if tt.wantFatal == nil {
				require.NoError(t, fatal)
				return
			}
			require.ErrorIs(t, fatal, tt.wantFatal)
		})
	}
}

func TestSummaryBreakerTripped(t *testing.T) {
	t.Parallel()

	for _, tt := range []struct {
		name  string
		count int
		want  bool
	}{
		{name: "fresh session", count: 0},
		{name: "one failure", count: 1},
		{name: "one below the limit", count: maxSummaryFailures - 1},
		{name: "at the limit", count: maxSummaryFailures, want: true},
		{name: "past the limit", count: maxSummaryFailures + 5, want: true},
	} {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			require.Equal(t, tt.want, summaryBreakerTripped(session.Session{SummaryFailCount: tt.count}))
		})
	}
}
