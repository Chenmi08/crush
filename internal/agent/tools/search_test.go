package tools

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"sync/atomic"
	"testing"
	"time"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// normalSearchPage is a minimal results page with one result.
const normalSearchPage = `<html><body><table>
<tr><td><a class="result-link" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpost">Example Post</a></td></tr>
<tr><td class="result-snippet">A snippet about the example post.</td></tr>
</table></body></html>`

// disableSearchRetryDelay removes the backoff wait so retry tests stay fast.
func disableSearchRetryDelay(t *testing.T) {
	t.Helper()
	orig := searchRetryDelay
	searchRetryDelay = 0
	t.Cleanup(func() { searchRetryDelay = orig })
}

// serveSearch points the DuckDuckGo endpoint at a local handler and disables
// the retry backoff for the duration of the test.
func serveSearch(t *testing.T, handler http.HandlerFunc) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(handler)
	t.Cleanup(srv.Close)
	orig := ddgLiteEndpoint
	ddgLiteEndpoint = srv.URL + "/lite/?q="
	t.Cleanup(func() { ddgLiteEndpoint = orig })
	disableSearchRetryDelay(t)
	return srv
}

// serveSearchStub points the DuckDuckGo endpoint at a local server
// returning the given status and body, and restores it on cleanup.
func serveSearchStub(t *testing.T, status int, body string) {
	t.Helper()
	serveSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(status)
		_, _ = w.Write([]byte(body))
	})
}

// loadAnomalyPage reads the real bot-check payload captured from
// lite.duckduckgo.com on 2026-07-29 (HTTP 202, captcha modal).
func loadAnomalyPage(t *testing.T) string {
	t.Helper()
	data, err := os.ReadFile("testdata/ddg_anomaly_202.html")
	require.NoError(t, err)
	return string(data)
}

// TestSearchRateLimitedOn202 verifies the 202 anomaly-challenge
// interstitial is reported as throttling, not parsed into an empty
// result set, even after retries are exhausted.
func TestSearchRateLimitedOn202(t *testing.T) {
	serveSearchStub(t, http.StatusAccepted, loadAnomalyPage(t))
	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "anything", 10)
	require.ErrorIs(t, err, errSearchRateLimited)
}

// TestSearchRateLimitedOnAnomalyPage verifies the captcha modal is
// detected by content as well: DuckDuckGo also serves the same page
// with HTTP 200 once a client is flagged.
func TestSearchRateLimitedOnAnomalyPage(t *testing.T) {
	serveSearchStub(t, http.StatusOK, loadAnomalyPage(t))
	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "anything", 10)
	require.ErrorIs(t, err, errSearchRateLimited)
}

// TestSearchRateLimitedOn429 verifies the canonical rate-limit status is
// mapped to the same actionable error.
func TestSearchRateLimitedOn429(t *testing.T) {
	serveSearchStub(t, http.StatusTooManyRequests, "slow down")
	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "anything", 10)
	require.ErrorIs(t, err, errSearchRateLimited)
}

// TestSearchParsesNormalResults guards against false positives: a
// genuine results page still parses.
func TestSearchParsesNormalResults(t *testing.T) {
	serveSearchStub(t, http.StatusOK, normalSearchPage)
	results, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "example", 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, "https://example.com/post", results[0].Link)
}

// TestSearchRetriesAfterThrottle verifies a transient bot check is retried
// once and the recovered results are returned.
func TestSearchRetriesAfterThrottle(t *testing.T) {
	anomaly := loadAnomalyPage(t)
	var requests atomic.Int32
	serveSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusAccepted)
			_, _ = w.Write([]byte(anomaly))
			return
		}
		_, _ = w.Write([]byte(normalSearchPage))
	})

	results, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "example", 10)
	require.NoError(t, err)
	require.Equal(t, int32(2), requests.Load())
	require.Len(t, results, 1)
}

// TestSearchRetriesOnServerError verifies a 5xx is treated as transient.
func TestSearchRetriesOnServerError(t *testing.T) {
	var requests atomic.Int32
	serveSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusBadGateway)
			return
		}
		_, _ = w.Write([]byte(normalSearchPage))
	})

	results, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "example", 10)
	require.NoError(t, err)
	require.Equal(t, int32(2), requests.Load())
	require.Len(t, results, 1)
}

// TestSearchDoesNotRetryOnClientError verifies a 4xx is surfaced
// immediately instead of being retried.
func TestSearchDoesNotRetryOnClientError(t *testing.T) {
	var requests atomic.Int32
	serveSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		requests.Add(1)
		w.WriteHeader(http.StatusBadRequest)
	})

	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "example", 10)
	require.Error(t, err)
	require.Equal(t, int32(1), requests.Load())
}

// TestSearchRetryExhaustedByClientError verifies that when a retry lands on
// a non-retryable error, that error is surfaced and is not mistaken for
// throttling.
func TestSearchRetryExhaustedByClientError(t *testing.T) {
	var requests atomic.Int32
	serveSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		if requests.Add(1) == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			return
		}
		w.WriteHeader(http.StatusBadRequest)
	})

	_, err := searchDuckDuckGo(context.Background(), http.DefaultClient, "example", 10)
	require.Error(t, err)
	require.NotErrorIs(t, err, errSearchRateLimited)
	require.Equal(t, int32(2), requests.Load())
}

// TestSearchCanceledContext verifies cancellation during backoff is
// reported as a context error rather than swallowing into a search error.
func TestSearchCanceledContext(t *testing.T) {
	serveSearch(t, func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusAccepted)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := searchDuckDuckGo(ctx, http.DefaultClient, "example", 10)
	require.ErrorIs(t, err, context.Canceled)
}

// TestSleepWithContext covers the immediate, timed, and canceled paths.
func TestSleepWithContext(t *testing.T) {
	require.NoError(t, sleepWithContext(context.Background(), 0))
	require.NoError(t, sleepWithContext(context.Background(), 5*time.Millisecond))

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	require.ErrorIs(t, sleepWithContext(ctx, time.Second), context.Canceled)
}

// TestSearchDeduplicatesResults verifies the same URL is only reported once.
func TestSearchDeduplicatesResults(t *testing.T) {
	page := `<html><body><table>
<tr><td><a class="result-link" href="https://example.com/same">First</a></td></tr>
<tr><td class="result-snippet">First snippet.</td></tr>
<tr><td><a class="result-link" href="https://example.com/same">Duplicate</a></td></tr>
<tr><td class="result-snippet">Duplicate snippet.</td></tr>
<tr><td><a class="result-link" href="https://example.com/other">Other</a></td></tr>
<tr><td class="result-snippet">Other snippet.</td></tr>
</table></body></html>`
	results, err := parseLiteSearchResults(page, 10)
	require.NoError(t, err)
	require.Len(t, results, 2)
	require.Equal(t, "https://example.com/same", results[0].Link)
	require.Equal(t, "https://example.com/other", results[1].Link)
	require.Equal(t, 1, results[0].Position)
	require.Equal(t, 2, results[1].Position)
}

// TestSearchSnippetCapped verifies oversized snippets are truncated to the
// configured rune budget.
func TestSearchSnippetCapped(t *testing.T) {
	long := strings.Repeat("a", maxSnippetRunes+100)
	page := `<html><body><table>
<tr><td><a class="result-link" href="https://example.com/long">Long</a></td></tr>
<tr><td class="result-snippet">` + long + `</td></tr>
</table></body></html>`

	results, err := parseLiteSearchResults(page, 10)
	require.NoError(t, err)
	require.Len(t, results, 1)
	require.Equal(t, maxSnippetRunes, utf8.RuneCountInString(results[0].Snippet))
}

// TestCleanDuckDuckGoURL covers the redirect link shapes DuckDuckGo emits.
func TestCleanDuckDuckGoURL(t *testing.T) {
	cases := []struct {
		name string
		in   string
		want string
	}{
		{
			name: "protocol relative",
			in:   "//duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fpost",
			want: "https://example.com/post",
		},
		{
			name: "absolute",
			in:   "https://duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fa%3Fb%3D1",
			want: "https://example.com/a?b=1",
		},
		{
			name: "root relative",
			in:   "/l/?uddg=https%3A%2F%2Fexample.com%2Frel",
			want: "https://example.com/rel",
		},
		{
			name: "subdomain",
			in:   "https://html.duckduckgo.com/l/?uddg=https%3A%2F%2Fexample.com%2Fsub",
			want: "https://example.com/sub",
		},
		{
			name: "plain url",
			in:   "https://example.com/plain",
			want: "https://example.com/plain",
		},
		{
			name: "foreign host not unwrapped",
			in:   "https://example.com/?uddg=https%3A%2F%2Fevil.example",
			want: "https://example.com/?uddg=https%3A%2F%2Fevil.example",
		},
		{
			name: "lookalike host not unwrapped",
			in:   "https://notduckduckgo.com/?uddg=https%3A%2F%2Fevil.example",
			want: "https://notduckduckgo.com/?uddg=https%3A%2F%2Fevil.example",
		},
	}

	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			require.Equal(t, tc.want, cleanDuckDuckGoURL(tc.in))
		})
	}
}

// TestIsRetryable verifies the retry classification is preserved through
// wrapping.
func TestIsRetryable(t *testing.T) {
	require.True(t, isRetryable(markRetryable(errors.New("boom"))))
	require.True(t, isRetryable(markRetryable(errSearchRateLimited)))
	require.False(t, isRetryable(errors.New("boom")))
	require.False(t, isRetryable(&searchStatusError{statusCode: http.StatusBadRequest}))
}
