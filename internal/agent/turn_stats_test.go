package agent

import (
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

func TestTurnStatsAggregatesSteps(t *testing.T) {
	t.Parallel()

	var turn turnStats

	// Step 1: 4s generation window, 500 output tokens, 30K prompt with
	// 29K cache reads.
	turn.addStep(stepMetrics{
		stats: message.Stats{
			TTFTMillis:          1200,
			OutputTokens:        500,
			CacheReadTokens:     29000,
			CacheCreationTokens: 500,
			TotalPromptTokens:   30000,
		},
		window: 4 * time.Second,
	})

	// Step 2: a slower first token than step 1 must not overwrite the
	// turn's TTFT; 2s window, 200 output tokens, larger context.
	turn.addStep(stepMetrics{
		stats: message.Stats{
			TTFTMillis:          300,
			OutputTokens:        200,
			CacheReadTokens:     33500,
			CacheCreationTokens: 100,
			TotalPromptTokens:   34000,
		},
		window: 2 * time.Second,
	})

	// Step 3: 3s window, 300 output tokens.
	turn.addStep(stepMetrics{
		stats: message.Stats{
			TTFTMillis:          400,
			OutputTokens:        300,
			CacheReadTokens:     34600,
			CacheCreationTokens: 50,
			TotalPromptTokens:   35000,
		},
		window: 3 * time.Second,
	})

	got := turn.snapshot()

	require.Equal(t, 1200*time.Millisecond, got.TTFT(), "TTFT must come from the first step")
	require.Equal(t, int64(1000), got.OutputTokens)
	require.Equal(t, int64(99000), got.TotalPromptTokens, "prompt tokens must be summed")
	require.Equal(t, int64(97100), got.CacheReadTokens)
	require.Equal(t, int64(650), got.CacheCreationTokens)
	require.InDelta(t, 1000.0/9.0, got.TokensPerSecond, 0.001, "speed must be weighted by generation time")
	require.InDelta(t, 97100.0/99000.0, got.CacheHitRate(), 1e-9)
	require.False(t, got.Estimated)
}

func TestTurnStatsKeepsFirstStepTTFT(t *testing.T) {
	t.Parallel()

	var turn turnStats
	turn.addStep(stepMetrics{stats: message.Stats{OutputTokens: 10}, window: time.Second})
	turn.addStep(stepMetrics{stats: message.Stats{TTFTMillis: 300, OutputTokens: 10}, window: time.Second})

	require.Zero(t, turn.snapshot().TTFTMillis,
		"a later step's TTFT must not stand in for an unobserved first-step TTFT")
}

func TestTurnStatsEstimatedWhenAnyStepEstimated(t *testing.T) {
	t.Parallel()

	var turn turnStats
	turn.addStep(stepMetrics{stats: message.Stats{OutputTokens: 10}, window: time.Second})
	turn.addStep(stepMetrics{stats: message.Stats{OutputTokens: 10, Estimated: true}, window: time.Second})

	require.True(t, turn.snapshot().Estimated)
}

func TestTurnStatsSingleStepMatchesStepStats(t *testing.T) {
	t.Parallel()

	base := time.Now()
	first := base.Add(800 * time.Millisecond)
	streamEnd := first.Add(2 * time.Second)
	usage := fantasy.Usage{
		InputTokens:         300,
		OutputTokens:        100,
		CacheReadTokens:     1200,
		CacheCreationTokens: 100,
	}
	metrics := computeStepStats(base, first, streamEnd, usage, usage, false)

	var turn turnStats
	turn.addStep(metrics)
	got := turn.snapshot()

	require.Equal(t, metrics.stats.TTFT(), got.TTFT())
	require.Equal(t, metrics.stats.OutputTokens, got.OutputTokens)
	require.Equal(t, metrics.stats.TotalPromptTokens, got.TotalPromptTokens)
	require.Equal(t, metrics.stats.CacheReadTokens, got.CacheReadTokens)
	require.Equal(t, metrics.stats.CacheCreationTokens, got.CacheCreationTokens)
	require.InDelta(t, metrics.stats.TokensPerSecond, got.TokensPerSecond, 0.001)
	require.InDelta(t, metrics.stats.CacheHitRate(), got.CacheHitRate(), 1e-9)
}
