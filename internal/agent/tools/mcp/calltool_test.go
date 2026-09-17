package mcp

import (
	"context"
	"errors"
	"testing"
	"time"

	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// errThrottled mirrors the transport error the MCP SDK produces for an
// HTTP 429.
var errThrottled = errors.New(
	`calling "tools/call": rejected by transport: sending "tools/call": Too Many Requests`,
)

// scriptedCaller returns a scripted sequence of results, one per attempt,
// and records how many attempts were made and when each one started. A nil
// entry means success.
type scriptedCaller struct {
	results []error
	calls   int
	times   []time.Time
}

func (s *scriptedCaller) CallTool(context.Context, *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	s.times = append(s.times, time.Now())
	i := s.calls
	s.calls++

	if i < len(s.results) && s.results[i] != nil {
		return nil, s.results[i]
	}
	return &sdkmcp.CallToolResult{}, nil
}

// shortenCooldown shrinks the backoff for the duration of one test. These
// tests deliberately avoid t.Parallel(): they mutate a package-level
// variable, and sequential tests are guaranteed to run before any parallel
// test resumes.
func shortenCooldown(t *testing.T) {
	t.Helper()

	previous := defaultCooldown
	defaultCooldown = time.Millisecond
	t.Cleanup(func() { defaultCooldown = previous })
}

func TestCallToolRetriesThrottledWithoutLimiter(t *testing.T) {
	shortenCooldown(t)

	caller := &scriptedCaller{results: []error{errThrottled, nil}}

	result, err := callTool(context.Background(), nil, "exa", caller, "web_search_exa", nil)
	require.NoError(t, err, "a transient throttle must not surface as a failure")
	require.NotNil(t, result)
	require.Equal(t, 2, caller.calls, "the call must be retried once")
}

func TestCallToolGivesUpAfterMaxAttempts(t *testing.T) {
	shortenCooldown(t)

	caller := &scriptedCaller{results: []error{errThrottled, errThrottled, errThrottled}}

	_, err := callTool(context.Background(), nil, "exa", caller, "web_search_exa", nil)
	require.ErrorIs(t, err, errThrottled)
	require.Equal(t, 3, caller.calls)
}

func TestCallToolDoesNotRetryOrdinaryErrors(t *testing.T) {
	shortenCooldown(t)

	boom := errors.New("connection reset by peer")
	caller := &scriptedCaller{results: []error{boom, nil}}

	_, err := callTool(context.Background(), nil, "exa", caller, "web_search_exa", nil)
	require.ErrorIs(t, err, boom)
	require.Equal(t, 1, caller.calls, "only throttling is worth retrying")
}

func TestCallToolRetriesThrottledWithLimiter(t *testing.T) {
	// This test observes the cooldown, so it needs one long enough to see;
	// it deliberately does not use shortenCooldown.
	previous := defaultCooldown
	defaultCooldown = 40 * time.Millisecond
	t.Cleanup(func() { defaultCooldown = previous })

	limit := 1000.0
	lim := limiterFor("calltool-retry", &limit, 1)
	require.NotNil(t, lim)

	caller := &scriptedCaller{results: []error{errThrottled, nil}}

	result, err := callTool(context.Background(), lim, "calltool-retry", caller, "web_search_exa", nil)
	require.NoError(t, err)
	require.NotNil(t, result)
	require.Equal(t, 2, caller.calls)

	// The throttle put the whole server into cooldown, so the queued retry
	// had to wait it out rather than retrying on its own schedule.
	require.GreaterOrEqual(t, caller.times[1].Sub(caller.times[0]), 30*time.Millisecond)

	_, _, ok := LimiterStatus("calltool-retry")
	require.True(t, ok)
}

func TestCallToolStopsWhenContextEnds(t *testing.T) {
	shortenCooldown(t)

	ctx, cancel := context.WithCancel(context.Background())
	caller := &cancellableCaller{cancel: cancel}

	_, err := callTool(ctx, nil, "exa", caller, "web_search_exa", nil)
	require.Error(t, err, "a cancelled context must abort the retry loop")
	require.LessOrEqual(t, caller.calls, 3)
}

// cancellableCaller throttles every attempt and cancels the context on the
// first one, proving the backoff respects cancellation.
type cancellableCaller struct {
	cancel context.CancelFunc
	calls  int
}

func (c *cancellableCaller) CallTool(context.Context, *sdkmcp.CallToolParams) (*sdkmcp.CallToolResult, error) {
	c.calls++
	c.cancel()
	return nil, errThrottled
}
