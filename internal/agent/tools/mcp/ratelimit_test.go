package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/stretchr/testify/require"
)

func TestLimiterForDisabled(t *testing.T) {
	t.Parallel()

	zero := 0.0
	negative := -1.0

	require.Nil(t, limiterFor("disabled-nil", nil, 3))
	require.Nil(t, limiterFor("disabled-zero", &zero, 3))
	require.Nil(t, limiterFor("disabled-negative", &negative, 3))
}

func TestLimiterForSharesOneInstancePerServer(t *testing.T) {
	t.Parallel()

	limit := 100.0
	first := limiterFor("shared", &limit, 1)
	second := limiterFor("shared", &limit, 1)

	require.NotNil(t, first)
	require.Same(t, first, second)
}

func TestLimiterForFollowsConfigChanges(t *testing.T) {
	t.Parallel()

	slow, fast := 1.0, 50.0
	l := limiterFor("reconfigured", &slow, 1)
	require.NotNil(t, l)
	require.InDelta(t, 1, float64(l.limiter.Limit()), 0.001)

	again := limiterFor("reconfigured", &fast, 3)
	require.Same(t, l, again)
	require.InDelta(t, 50, float64(again.limiter.Limit()), 0.001)
	require.Equal(t, 3, again.limiter.Burst())
}

func TestLimiterPacesCalls(t *testing.T) {
	t.Parallel()

	// 20 req/s is one token every 50ms.
	limit := 20.0
	l := limiterFor("paced", &limit, 1)
	require.NotNil(t, l)

	ctx := context.Background()
	require.NoError(t, l.acquire(ctx)) // Spends the initial burst token.

	start := time.Now()
	require.NoError(t, l.acquire(ctx))
	require.NoError(t, l.acquire(ctx))

	// Two further tokens must be spaced out; allow slack for scheduling.
	require.GreaterOrEqual(t, time.Since(start), 90*time.Millisecond)
}

func TestLimiterCooldownBlocksAcquire(t *testing.T) {
	t.Parallel()

	limit := 1000.0
	l := limiterFor("cooling", &limit, 1)
	require.NotNil(t, l)

	l.cooldown(60 * time.Millisecond)

	start := time.Now()
	require.NoError(t, l.acquire(context.Background()))
	require.GreaterOrEqual(t, time.Since(start), 55*time.Millisecond)

	_, _, ok := LimiterStatus("cooling")
	require.True(t, ok)
}

func TestLimiterStatusUnknownServer(t *testing.T) {
	t.Parallel()

	_, _, ok := LimiterStatus("never-configured")
	require.False(t, ok)
}

func TestLimiterAcquireGivesUpWithContext(t *testing.T) {
	t.Parallel()

	// One token every two seconds.
	limit := 0.5
	l := limiterFor("ctx-bound", &limit, 1)
	require.NotNil(t, l)
	require.NoError(t, l.acquire(context.Background())) // Spends the initial token.

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Millisecond)
	defer cancel()

	start := time.Now()
	require.Error(t, l.acquire(ctx))
	require.Less(t, time.Since(start), time.Second)
}

func TestIsRateLimited(t *testing.T) {
	t.Parallel()

	require.False(t, isRateLimited(nil))
	require.False(t, isRateLimited(errors.New("connection reset by peer")))
	require.True(t, isRateLimited(errors.New(
		`calling "tools/call": rejected by transport: sending "tools/call": Too Many Requests`,
	)))
	require.True(t, isRateLimited(errors.New("unexpected status 429")))
}
