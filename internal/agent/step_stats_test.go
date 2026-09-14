package agent

import (
	"testing"
	"time"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"
)

func TestComputeStepStats(t *testing.T) {
	t.Parallel()

	start := time.Now()
	firstToken := start.Add(800 * time.Millisecond)
	streamEnd := firstToken.Add(2 * time.Second)
	usage := fantasy.Usage{
		InputTokens:         300,
		OutputTokens:        100,
		CacheReadTokens:     1200,
		CacheCreationTokens: 100,
	}

	metrics := computeStepStats(start, firstToken, streamEnd, usage, usage, false)
	stats := metrics.stats

	require.Equal(t, 2*time.Second, metrics.window)
	require.False(t, stats.Estimated)
	require.Equal(t, 800*time.Millisecond, stats.TTFT())
	require.InDelta(t, 50.0, stats.TokensPerSecond, 0.001)
	require.Equal(t, int64(1200), stats.CacheReadTokens)
	require.Equal(t, int64(100), stats.CacheCreationTokens)
	require.Equal(t, int64(1600), stats.TotalPromptTokens)
	require.InDelta(t, 0.75, stats.CacheHitRate(), 0.001)
	require.Equal(t, int64(100), stats.OutputTokens)
}

func TestComputeStepStatsCarriesEstimated(t *testing.T) {
	t.Parallel()

	stats := computeStepStats(time.Now(), time.Time{}, time.Time{}, fantasy.Usage{}, fantasy.Usage{}, true).stats
	require.True(t, stats.Estimated)
}

func TestComputeStepStatsFallsBackToStepUsage(t *testing.T) {
	t.Parallel()

	start := time.Now()
	firstToken := start.Add(time.Second)
	streamEnd := firstToken.Add(4 * time.Second)
	// The provider reported no usage at stream finish, so the estimated
	// step usage supplies the output token count.
	usage := fantasy.Usage{InputTokens: 400, OutputTokens: 200}

	stats := computeStepStats(start, firstToken, streamEnd, fantasy.Usage{}, usage, true).stats

	require.Equal(t, time.Second, stats.TTFT())
	require.InDelta(t, 50.0, stats.TokensPerSecond, 0.001)
	require.Zero(t, stats.CacheReadTokens)
	require.Equal(t, int64(400), stats.TotalPromptTokens)
	require.Zero(t, stats.CacheHitRate())
	require.Equal(t, int64(200), stats.OutputTokens)
	require.True(t, stats.Estimated)
}

func TestComputeStepStatsWithoutFirstToken(t *testing.T) {
	t.Parallel()

	start := time.Now()
	usage := fantasy.Usage{InputTokens: 500, CacheReadTokens: 500}

	metrics := computeStepStats(start, time.Time{}, start.Add(time.Second), fantasy.Usage{}, usage, false)
	stats := metrics.stats

	require.Zero(t, metrics.window, "no first token means no generation window")
	require.Zero(t, stats.TTFT())
	require.Zero(t, stats.TokensPerSecond)
	require.Equal(t, int64(500), stats.CacheReadTokens)
	require.Equal(t, int64(1000), stats.TotalPromptTokens)
	require.InDelta(t, 0.5, stats.CacheHitRate(), 0.001)
}

func TestComputeStepStatsIgnoresWindowBeforeRetry(t *testing.T) {
	t.Parallel()

	// On retry stepStart is moved past the backoff delay. A stream that
	// somehow finishes before that point must not yield a negative TTFT
	// or a bogus speed.
	start := time.Now().Add(5 * time.Second)
	firstToken := time.Now()
	streamEnd := firstToken.Add(time.Second)
	usage := fantasy.Usage{OutputTokens: 10}

	metrics := computeStepStats(start, firstToken, streamEnd, usage, usage, false)
	stats := metrics.stats

	require.Zero(t, metrics.window)
	require.Zero(t, stats.TTFT())
	require.Zero(t, stats.TokensPerSecond)
}
