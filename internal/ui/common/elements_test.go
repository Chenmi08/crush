package common

import (
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
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

func TestFormatTurnStatsJoinsRowsWhenWide(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	stats := message.Stats{
		TTFTMillis:          800,
		TokensPerSecond:     42.3,
		CacheReadTokens:     12_420,
		CacheCreationTokens: 100,
		TotalPromptTokens:   13_500,
		OutputTokens:        1200,
	}

	// On a wide row the timing and token stats fit together, so the
	// block costs a single line under the assistant footer.
	actual := ansi.Strip(FormatTurnStats(&sty, stats, 60, 0))

	require.Equal(t, "0.8s · 42.3 tok/s · ↑13.5K 92.00% · ↓1.2K", actual)
}

func TestFormatTurnStatsSplitsRowsWhenNarrow(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	stats := message.Stats{
		TTFTMillis:        800,
		TokensPerSecond:   42.3,
		CacheReadTokens:   12_420,
		TotalPromptTokens: 13_500,
		OutputTokens:      1200,
	}

	// 18 columns are too little for the whole token row, so it splits
	// into one stat per line.
	actual := ansi.Strip(FormatTurnStats(&sty, stats, 18, 0))

	require.Equal(t, "0.8s · 42.3 tok/s\n↑13.5K 92.00%\n↓1.2K", actual)
}

func TestFormatTurnStatsGroupsTokensWhenWideEnough(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	stats := message.Stats{
		TTFTMillis:        2200,
		TokensPerSecond:   203,
		CacheReadTokens:   13_860,
		TotalPromptTokens: 14_000,
		OutputTokens:      1300,
	}

	// 27 columns fit the token row, keeping the cache and the generated
	// token count together.
	actual := ansi.Strip(FormatTurnStats(&sty, stats, 27, 0))

	require.Equal(t, "2.2s · 203 tok/s\n↑14K 99.00% · ↓1.3K", actual)
}

func TestFormatTurnStatsKeepsTokenRowAtLargeCounts(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	stats := message.Stats{
		TTFTMillis:        2200,
		TokensPerSecond:   203,
		CacheReadTokens:   104_500,
		TotalPromptTokens: 115_200,
		OutputTokens:      123_400,
	}

	// Six-character token counts used to push the output tokens onto
	// their own row; the input-plus-rate form always fits.
	actual := ansi.Strip(FormatTurnStats(&sty, stats, 27, 0))

	require.Equal(t, "2.2s · 203 tok/s\n↑115.2K 90.71% · ↓123.4K", actual)
}

func TestFormatCacheStatsHitRatePrecision(t *testing.T) {
	t.Parallel()

	// Non-round hit rates keep two decimal places.
	stats := message.Stats{
		CacheReadTokens:   12_400,
		TotalPromptTokens: 13_500,
	}

	require.Equal(t, "↑13.5K 91.85%", formatCacheStats(stats))
}

func TestFormatTurnStatsOmitsCacheWithoutInputTotal(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	// The input total anchors the hit rate; a lone cache read count is
	// not shown as if it were the input.
	actual := ansi.Strip(FormatTurnStats(&sty, message.Stats{CacheReadTokens: 500}, 60, 0))

	require.Empty(t, actual)
}

func TestFormatTurnStatsOutputOnly(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	actual := ansi.Strip(FormatTurnStats(&sty, message.Stats{OutputTokens: 1200}, 60, 0))

	require.Equal(t, "↓1.2K", actual)
}

func TestFormatTurnStatsOmitsMissingValues(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	require.Empty(t, FormatTurnStats(&sty, message.Stats{}, 30, 0))

	actual := ansi.Strip(FormatTurnStats(&sty, message.Stats{
		TokensPerSecond: 10.0,
		Estimated:       true,
	}, 30, 0))
	require.Equal(t, "~10.0 tok/s", actual)

	actual = ansi.Strip(FormatTurnStats(&sty, message.Stats{TTFTMillis: 75}, 30, 0))
	require.Equal(t, "75ms", actual)
}

func TestFormatTurnStatsShowsPromptWithoutCacheReads(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	// Prompt tokens are shown even on a full cache miss so the turn row
	// stays the increment of the session totals.
	actual := ansi.Strip(FormatTurnStats(&sty, message.Stats{
		TotalPromptTokens: 1200,
		OutputTokens:      300,
	}, 60, 0))

	require.Equal(t, "↑1.2K · ↓300", actual)
}

func TestFormatTurnStatsIndent(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	stats := message.Stats{
		TTFTMillis:        800,
		TokensPerSecond:   42.3,
		CacheReadTokens:   12_420,
		TotalPromptTokens: 13_500,
		OutputTokens:      1200,
	}

	out := ansi.Strip(FormatTurnStats(&sty, stats, 60, 2))

	for _, line := range strings.Split(out, "\n") {
		require.True(t, strings.HasPrefix(line, "  "), "line %q must be indented", line)
		require.Equal(t, strings.TrimRight(line, " "), line, "line %q must not be padded", line)
	}
}

func TestFormatSessionTotalsOnly(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	// Session totals outlive the per-turn stats (for example after a
	// restart), so they render on their own in the sidebar.
	actual := ansi.Strip(renderStatGroups(sty.ModelInfo.Stats, 58, sessionTotalParts(session.SessionTokens{
		InputTokens:     100,
		OutputTokens:    50,
		CacheReadTokens: 900,
	}, false)))

	require.Equal(t, "Σ ↑1K 90.00% · ↓50", actual)
}

func TestFormatSessionTotalsSplitsWhenNarrow(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	actual := ansi.Strip(renderStatGroups(sty.ModelInfo.Stats, 18, sessionTotalParts(session.SessionTokens{
		InputTokens:     20_000,
		OutputTokens:    45_600,
		CacheReadTokens: 1_200_000,
	}, false)))

	require.Equal(t, "Σ ↑1.2M 98.36%\n↓45.6K", actual)
}

func TestSessionTotalParts(t *testing.T) {
	t.Parallel()

	// The hit rate is omitted when nothing was served from the cache,
	// but the total prompt count still shows.
	parts := sessionTotalParts(session.SessionTokens{
		InputTokens:  1200,
		OutputTokens: 300,
	}, false)
	require.Equal(t, []string{"Σ ↑1.2K", "↓300"}, parts)

	// Estimated output is marked with a tilde.
	parts = sessionTotalParts(session.SessionTokens{OutputTokens: 1200}, true)
	require.Equal(t, []string{"↓~1.2K"}, parts)

	require.Empty(t, sessionTotalParts(session.SessionTokens{}, false))
}

func TestModelInfoOmitsTurnStats(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	rendered := ansi.Strip(ModelInfo(&sty, "model", "", "", &ModelContextInfo{
		ContextUsed:  120,
		ModelContext: 1000,
	}, 80, nil))

	require.NotContains(t, rendered, "tok/s", "per-turn stats live under the assistant footer now")
	require.NotContains(t, rendered, "↑")
}

func TestModelInfoRendersSessionTotals(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	rendered := ansi.Strip(ModelInfo(&sty, "model", "", "", &ModelContextInfo{
		ContextUsed:  120,
		ModelContext: 1000,
		Totals: session.SessionTokens{
			InputTokens:     1000,
			OutputTokens:    2000,
			CacheReadTokens: 9000,
		},
	}, 80, nil))

	require.Contains(t, rendered, "Σ ↑10K 90.00% · ↓2K")
}
