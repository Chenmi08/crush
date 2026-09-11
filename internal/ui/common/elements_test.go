package common

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestFormatTokensAndCostPrefixesEstimatedUsage(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	rendered := formatTokensAndCost(&sty, 120, 1000, 0, true)
	actual := ansi.Strip(rendered)

	require.Contains(t, actual, "~12%")
	require.Contains(t, actual, "(120)")
	require.Contains(t, actual, "$0.00")
	require.True(t, strings.Contains(rendered, sty.ModelInfo.TokenPercentage.Render("~12%")))
}

func TestFormatTokensAndCostOmitsEstimatedPrefix(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	actual := ansi.Strip(formatTokensAndCost(&sty, 120, 1000, 0, false))

	require.Contains(t, actual, "12%")
	require.NotContains(t, actual, "~12%")
}

func TestFormatStepStatsKeepsTokenRowSeparate(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	ctx := &ModelContextInfo{
		Stats: session.StepStats{
			TTFT:                800 * time.Millisecond,
			TokensPerSecond:     42.3,
			CacheReadTokens:     12_400,
			CacheCreationTokens: 100,
			TotalPromptTokens:   13_500,
			CacheHitRate:        0.92,
			OutputTokens:        1200,
		},
	}

	// Even on a wide row the token accounting never shares a line with
	// the timing stats.
	actual := ansi.Strip(formatStepStats(&sty, ctx, 60))

	require.Equal(t, "0.8s · 42.3 tok/s\n↑13.5K 92.00% · ↓1.2K", actual)
}

func TestFormatStepStatsSplitsRowsWhenNarrow(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	ctx := &ModelContextInfo{
		Stats: session.StepStats{
			TTFT:              800 * time.Millisecond,
			TokensPerSecond:   42.3,
			CacheReadTokens:   12_400,
			TotalPromptTokens: 13_500,
			CacheHitRate:      0.92,
			OutputTokens:      1200,
		},
	}

	// Width 20 leaves 18 columns for stats, too little for the whole
	// token row, so it splits into one stat per line.
	actual := ansi.Strip(formatStepStats(&sty, ctx, 20))

	require.Equal(t, "0.8s · 42.3 tok/s\n↑13.5K 92.00%\n↓1.2K", actual)
}

func TestFormatStepStatsGroupsTokensAtSidebarWidth(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	ctx := &ModelContextInfo{
		Stats: session.StepStats{
			TTFT:              2200 * time.Millisecond,
			TokensPerSecond:   203,
			CacheReadTokens:   13_800,
			TotalPromptTokens: 14_000,
			CacheHitRate:      0.99,
			OutputTokens:      1300,
		},
	}

	// This mirrors the sidebar at its real width: 29 columns of content
	// and 27 usable for stats. The token row wraps below the timing row,
	// keeping the cache and the generated token count together.
	actual := ansi.Strip(formatStepStats(&sty, ctx, 29))

	require.Equal(t, "2.2s · 203 tok/s\n↑14K 99.00% · ↓1.3K", actual)
}

func TestFormatStepStatsKeepsTokenRowAtLargeCounts(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	ctx := &ModelContextInfo{
		Stats: session.StepStats{
			TTFT:              2200 * time.Millisecond,
			TokensPerSecond:   203,
			CacheReadTokens:   104_500,
			TotalPromptTokens: 115_200,
			CacheHitRate:      0.9071,
			OutputTokens:      123_400,
		},
	}

	// Six-character token counts used to push the output tokens onto
	// their own row; the input-plus-rate form always fits.
	actual := ansi.Strip(formatStepStats(&sty, ctx, 29))

	require.Equal(t, "2.2s · 203 tok/s\n↑115.2K 90.71% · ↓123.4K", actual)
}

func TestFormatCacheStatsHitRatePrecision(t *testing.T) {
	t.Parallel()

	// Non-round hit rates keep two decimal places.
	stats := session.StepStats{
		CacheReadTokens:   12_400,
		TotalPromptTokens: 13_500,
		CacheHitRate:      0.9185,
	}

	require.Equal(t, "↑13.5K 91.85%", formatCacheStats(stats))
}

func TestFormatStepStatsOmitsCacheWithoutInputTotal(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	// The input total anchors the hit rate; a lone cache read count is
	// not shown as if it were the input.
	actual := ansi.Strip(formatStepStats(&sty, &ModelContextInfo{
		Stats: session.StepStats{CacheReadTokens: 500},
	}, 60))

	require.Empty(t, actual)
}

func TestFormatStepStatsOutputOnly(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	actual := ansi.Strip(formatStepStats(&sty, &ModelContextInfo{
		Stats: session.StepStats{OutputTokens: 1200},
	}, 60))

	require.Equal(t, "↓1.2K", actual)
}

func TestFormatStepStatsOmitsMissingValues(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	require.Empty(t, formatStepStats(&sty, &ModelContextInfo{}, 30))

	actual := ansi.Strip(formatStepStats(&sty, &ModelContextInfo{
		EstimatedUsage: true,
		Stats:          session.StepStats{TokensPerSecond: 10.0},
	}, 30))
	require.Equal(t, "~10.0 tok/s", actual)

	actual = ansi.Strip(formatStepStats(&sty, &ModelContextInfo{
		Stats: session.StepStats{TTFT: 75 * time.Millisecond},
	}, 30))
	require.Equal(t, "75ms", actual)
}

func TestModelInfoRendersStepStats(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	rendered := ansi.Strip(ModelInfo(&sty, "model", "", "", &ModelContextInfo{
		ContextUsed:  120,
		ModelContext: 1000,
		Stats: session.StepStats{
			TTFT:              800 * time.Millisecond,
			TokensPerSecond:   42.3,
			CacheReadTokens:   12_400,
			TotalPromptTokens: 13_500,
			CacheHitRate:      0.92,
			OutputTokens:      1200,
		},
	}, 80, nil))

	// The timing and token rows must be separate lines.
	require.Contains(t, rendered, "0.8s · 42.3 tok/s")
	require.Contains(t, rendered, "↑13.5K 92.00% · ↓1.2K")
	require.NotContains(t, rendered, "0.8s · 42.3 tok/s · ↑")
}
