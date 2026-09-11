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

func TestFormatStepStatsCompactSingleLine(t *testing.T) {
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

	actual := ansi.Strip(formatStepStats(&sty, ctx, 60))

	require.Equal(t, "0.8s · 42.3 tok/s · ↺12.4K/13.5K (92%) · out 1.2K", actual)
}

func TestFormatStepStatsWrapsWhenNarrow(t *testing.T) {
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

	// The default sidebar content width is 30. The cache details must
	// wrap onto their own line instead of being dropped or truncated.
	actual := ansi.Strip(formatStepStats(&sty, ctx, 30))

	require.Equal(t, "0.8s · 42.3 tok/s · out 1.2K\n↺12.4K/13.5K (92%)", actual)
}

func TestFormatStepStatsCacheWithoutTotal(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	// Providers that do not report an input breakdown still show the
	// raw cache read count.
	actual := ansi.Strip(formatStepStats(&sty, &ModelContextInfo{
		Stats: session.StepStats{CacheReadTokens: 500},
	}, 60))

	require.Equal(t, "↺500", actual)
}

func TestFormatStepStatsOutputOnly(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()

	actual := ansi.Strip(formatStepStats(&sty, &ModelContextInfo{
		Stats: session.StepStats{OutputTokens: 1200},
	}, 60))

	require.Equal(t, "out 1.2K", actual)
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

	require.Contains(t, rendered, "0.8s · 42.3 tok/s · ↺12.4K/13.5K (92%) · out 1.2K")
}
