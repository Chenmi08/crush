package mcp

import (
	"context"
	"strings"
	"sync"
	"time"

	"golang.org/x/time/rate"
)

// defaultCooldown is how long every call to a server pauses after the
// server answers with a rate-limit error. The MCP SDK collapses an HTTP
// 429 into a transport error and never exposes the response headers, so
// Retry-After cannot be read; the Exa MCP endpoint reports "1", which is
// what this mirrors.
//
// It is a variable so tests can shorten it.
var defaultCooldown = time.Second

// rateLimitMarkers identify a throttled response. The MCP SDK reduces an
// HTTP 429 to an error string such as `rejected by transport: sending
// "tools/call": Too Many Requests`, so text matching is the only signal
// available to us.
var rateLimitMarkers = []string{
	"Too Many Requests",
	"429",
}

// limitersMu guards limiters. A plain map is used rather than csync.Map
// because its GetOrSet is a check-then-set: two concurrent callers could
// each receive a different limiter, which would silently double the
// effective rate. The critical section here is a map lookup, so a mutex
// is both correct and cheap.
var (
	limitersMu sync.Mutex
	limiters   = map[string]*serverLimiter{}
)

// serverLimiter wraps a token bucket with a cooldown engaged when the
// upstream reports throttling. One limiter exists per MCP server, shared
// process-wide: every agent draws from the same upstream quota, so they
// must share the same queue.
type serverLimiter struct {
	limiter *rate.Limiter

	mu            sync.Mutex
	cooldownUntil time.Time
	queued        int
}

// limiterFor returns the shared limiter for name, creating it on first
// use and keeping it in step with later configuration changes. It returns
// nil when the server has no rate limit configured.
func limiterFor(name string, rateLimit *float64, burst int) *serverLimiter {
	if rateLimit == nil || *rateLimit <= 0 {
		return nil
	}
	if burst <= 0 {
		burst = 1
	}

	limitersMu.Lock()
	defer limitersMu.Unlock()

	if l, ok := limiters[name]; ok {
		l.limiter.SetLimit(rate.Limit(*rateLimit))
		l.limiter.SetBurst(burst)
		return l
	}

	l := &serverLimiter{limiter: rate.NewLimiter(rate.Limit(*rateLimit), burst)}
	limiters[name] = l
	return l
}

// acquire blocks until the limiter grants a slot or ctx ends.
func (l *serverLimiter) acquire(ctx context.Context) error {
	l.bump(1)
	defer l.bump(-1)

	// Wait out any cooldown before taking a token so a throttled server
	// is not hammered by everything already queued behind it.
	for {
		l.mu.Lock()
		wait := time.Until(l.cooldownUntil)
		l.mu.Unlock()

		if wait <= 0 {
			break
		}
		if err := sleepCtx(ctx, wait); err != nil {
			return err
		}
	}

	return l.limiter.Wait(ctx)
}

// cooldown pauses every call to the server for d, extending any cooldown
// already in effect.
func (l *serverLimiter) cooldown(d time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if until := time.Now().Add(d); until.After(l.cooldownUntil) {
		l.cooldownUntil = until
	}
}

// status reports the current queue depth and, while the server is cooling
// down, how much longer that will last.
func (l *serverLimiter) status() (queued int, cooling time.Duration) {
	l.mu.Lock()
	defer l.mu.Unlock()

	if d := time.Until(l.cooldownUntil); d > 0 {
		cooling = d
	}
	return l.queued, cooling
}

func (l *serverLimiter) bump(delta int) {
	l.mu.Lock()
	defer l.mu.Unlock()

	l.queued += delta
}

// isRateLimited reports whether err looks like an upstream throttling
// response.
func isRateLimited(err error) bool {
	if err == nil {
		return false
	}
	msg := err.Error()
	for _, marker := range rateLimitMarkers {
		if strings.Contains(msg, marker) {
			return true
		}
	}
	return false
}

// LimiterStatus reports the queue depth and remaining cooldown for the
// named MCP server, for surfacing in the UI. The boolean is false when
// the server has no limiter.
func LimiterStatus(name string) (queued int, cooling time.Duration, ok bool) {
	limitersMu.Lock()
	l, exists := limiters[name]
	limitersMu.Unlock()

	if !exists {
		return 0, 0, false
	}
	queued, cooling = l.status()
	return queued, cooling, true
}

func sleepCtx(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	t := time.NewTimer(d)
	defer t.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-t.C:
		return nil
	}
}
