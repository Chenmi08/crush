package tools

import (
	"context"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math/rand/v2"
	"net/http"
	"net/url"
	"slices"
	"strings"
	"sync"
	"time"

	"golang.org/x/net/html"
)

// SearchResult represents a single search result from DuckDuckGo.
type SearchResult struct {
	Title    string
	Link     string
	Snippet  string
	Position int
}

var userAgents = []string{
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/129.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/130.0.0.0 Safari/537.36",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64; rv:132.0) Gecko/20100101 Firefox/132.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10.15; rv:133.0) Gecko/20100101 Firefox/133.0",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/18.1 Safari/605.1.15",
	"Mozilla/5.0 (Macintosh; Intel Mac OS X 10_15_7) AppleWebKit/605.1.15 (KHTML, like Gecko) Version/17.6 Safari/605.1.15",
	"Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 (KHTML, like Gecko) Chrome/131.0.0.0 Safari/537.36 Edg/131.0.0.0",
}

var acceptLanguages = []string{
	"en-US,en;q=0.9",
	"en-US,en;q=0.9,es;q=0.8",
	"en-GB,en;q=0.9,en-US;q=0.8",
	"en-US,en;q=0.5",
	"en-CA,en;q=0.9,en-US;q=0.8",
}

// errSearchRateLimited reports that DuckDuckGo served a bot-check page
// instead of results.
var errSearchRateLimited = errors.New(
	"DuckDuckGo is rate-limiting this machine. " +
		"Do not retry or rephrase; wait a few minutes or fetch known URLs directly",
)

// ddgAnomalyMarkers are substrings of the bot-detection page DuckDuckGo
// Lite serves (with HTTP 200) instead of results once a client trips its
// rate limiter.  Parsing that page yields zero results, which would
// otherwise masquerade as "your query found nothing".
var ddgAnomalyMarkers = []string{
	"anomaly-modal",
	"/anomaly.js",
	"Unfortunately, bots use DuckDuckGo too",
}

// ddgLiteEndpoint is a package var so tests can point the search at a
// local httptest server.
var ddgLiteEndpoint = "https://lite.duckduckgo.com/lite/?q="

const (
	// searchRetryAttempts bounds how many times one query is sent before
	// giving up. DuckDuckGo's bot checks are sometimes transient, so a
	// single retry with a fresh User-Agent is worthwhile; retrying more
	// would make throttling worse.
	searchRetryAttempts = 2

	// maxSnippetRunes caps how much of a search snippet reaches the model.
	// Snippets are forwarded verbatim into the sub-agent context, so an
	// unbounded one quietly inflates token usage.
	maxSnippetRunes = 500
)

// searchRetryDelay is the pause before a retry. It is a variable so tests
// can disable the wait.
var searchRetryDelay = time.Second

// retryableError marks a search failure that a fresh attempt could recover
// from, such as throttling, a transport error, or a 5xx response.
type retryableError struct{ err error }

func (e *retryableError) Error() string { return e.err.Error() }
func (e *retryableError) Unwrap() error { return e.err }

func markRetryable(err error) error { return &retryableError{err: err} }

func isRetryable(err error) bool {
	var retryable *retryableError
	return errors.As(err, &retryable)
}

// searchStatusError reports a non-OK response without a dedicated meaning
// (202 and 200 are handled separately).
type searchStatusError struct{ statusCode int }

func (e *searchStatusError) Error() string {
	return fmt.Sprintf("search failed with status code: %d", e.statusCode)
}

// searchDuckDuckGo runs the query, retrying once on a transient failure.
// The final error is returned unwrapped enough that callers can still
// detect errSearchRateLimited with errors.Is.
func searchDuckDuckGo(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, error) {
	if maxResults <= 0 {
		maxResults = 10
	}

	var lastErr error
	for attempt := 1; attempt <= searchRetryAttempts; attempt++ {
		results, err := searchDuckDuckGoOnce(ctx, client, query, maxResults)
		if err == nil {
			return results, nil
		}
		lastErr = err

		if attempt == searchRetryAttempts || !isRetryable(err) {
			break
		}

		slog.Debug("Retrying web search", "attempt", attempt, "error", err)
		if err := sleepWithContext(ctx, searchRetryDelay); err != nil {
			return nil, err
		}
	}

	return nil, lastErr
}

// searchDuckDuckGoOnce performs a single request attempt.
func searchDuckDuckGoOnce(ctx context.Context, client *http.Client, query string, maxResults int) ([]SearchResult, error) {
	searchURL := ddgLiteEndpoint + url.QueryEscape(query)

	req, err := http.NewRequestWithContext(ctx, "GET", searchURL, nil)
	if err != nil {
		return nil, fmt.Errorf("failed to create request: %w", err)
	}

	setRandomizedHeaders(req)

	resp, err := client.Do(req)
	if err != nil {
		return nil, markRetryable(fmt.Errorf("failed to execute search: %w", err))
	}
	defer resp.Body.Close()

	// A 202 from DuckDuckGo is the anomaly-challenge interstitial, not a
	// result page; report throttling rather than parsing it into an
	// empty result set. 429 is the canonical rate-limit status and is
	// treated the same in case DDG switches to it.
	if resp.StatusCode == http.StatusAccepted || resp.StatusCode == http.StatusTooManyRequests {
		return nil, markRetryable(errSearchRateLimited)
	}
	if resp.StatusCode != http.StatusOK {
		err := &searchStatusError{statusCode: resp.StatusCode}
		if resp.StatusCode >= http.StatusInternalServerError {
			return nil, markRetryable(err)
		}
		return nil, err
	}

	body, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, markRetryable(fmt.Errorf("failed to read response: %w", err))
	}

	content := string(body)
	for _, marker := range ddgAnomalyMarkers {
		if strings.Contains(content, marker) {
			return nil, markRetryable(errSearchRateLimited)
		}
	}

	return parseLiteSearchResults(content, maxResults)
}

// sleepWithContext waits for d unless the context is cancelled first.
func sleepWithContext(ctx context.Context, d time.Duration) error {
	if d <= 0 {
		return ctx.Err()
	}

	timer := time.NewTimer(d)
	defer timer.Stop()

	select {
	case <-ctx.Done():
		return ctx.Err()
	case <-timer.C:
		return nil
	}
}

func setRandomizedHeaders(req *http.Request) {
	req.Header.Set("User-Agent", userAgents[rand.IntN(len(userAgents))])
	req.Header.Set("Accept", "text/html,application/xhtml+xml,application/xml;q=0.9,*/*;q=0.8")
	req.Header.Set("Accept-Language", acceptLanguages[rand.IntN(len(acceptLanguages))])
	req.Header.Set("Accept-Encoding", "identity")
	req.Header.Set("Connection", "keep-alive")
	req.Header.Set("Upgrade-Insecure-Requests", "1")
	req.Header.Set("Sec-Fetch-Dest", "document")
	req.Header.Set("Sec-Fetch-Mode", "navigate")
	req.Header.Set("Sec-Fetch-Site", "none")
	req.Header.Set("Sec-Fetch-User", "?1")
	req.Header.Set("Cache-Control", "max-age=0")
	if rand.IntN(2) == 0 {
		req.Header.Set("DNT", "1")
	}
}

func parseLiteSearchResults(htmlContent string, maxResults int) ([]SearchResult, error) {
	doc, err := html.Parse(strings.NewReader(htmlContent))
	if err != nil {
		return nil, fmt.Errorf("failed to parse HTML: %w", err)
	}

	var results []SearchResult
	seen := make(map[string]bool)
	var currentResult *SearchResult

	// flush appends the in-progress result, dropping empty and duplicate
	// links so the same page is not reported twice.
	flush := func() {
		if currentResult == nil {
			return
		}
		result := currentResult
		currentResult = nil
		if result.Link == "" || seen[result.Link] {
			return
		}
		seen[result.Link] = true
		result.Position = len(results) + 1
		results = append(results, *result)
	}

	var traverse func(*html.Node)
	traverse = func(n *html.Node) {
		if n.Type == html.ElementNode {
			if n.Data == "a" && hasClass(n, "result-link") {
				flush()
				if len(results) >= maxResults {
					return
				}
				currentResult = &SearchResult{Title: getTextContent(n)}
				for _, attr := range n.Attr {
					if attr.Key == "href" {
						currentResult.Link = cleanDuckDuckGoURL(attr.Val)
						break
					}
				}
			}
			if n.Data == "td" && hasClass(n, "result-snippet") && currentResult != nil {
				currentResult.Snippet = truncateRunes(getTextContent(n), maxSnippetRunes)
			}
		}
		for c := n.FirstChild; c != nil; c = c.NextSibling {
			if len(results) >= maxResults {
				return
			}
			traverse(c)
		}
	}

	traverse(doc)

	if len(results) < maxResults {
		flush()
	}

	return results, nil
}

func hasClass(n *html.Node, class string) bool {
	for _, attr := range n.Attr {
		if attr.Key == "class" {
			if slices.Contains(strings.Fields(attr.Val), class) {
				return true
			}
		}
	}
	return false
}

func getTextContent(n *html.Node) string {
	var text strings.Builder
	var traverse func(*html.Node)
	traverse = func(node *html.Node) {
		if node.Type == html.TextNode {
			text.WriteString(node.Data)
		}
		for c := node.FirstChild; c != nil; c = c.NextSibling {
			traverse(c)
		}
	}
	traverse(n)
	return strings.TrimSpace(text.String())
}

// cleanDuckDuckGoURL unwraps DuckDuckGo's click-tracking redirect
// (/l/?uddg=<encoded>) into the real destination. DDG emits the link as
// protocol-relative, absolute, or root-relative depending on the page, so
// match on the parameter instead of a fixed prefix. Links on other hosts
// are returned untouched to avoid unwrapping unrelated URLs that happen
// to carry a uddg query parameter.
func cleanDuckDuckGoURL(rawURL string) string {
	if !strings.Contains(rawURL, "uddg=") {
		return rawURL
	}

	parsed, err := url.Parse(rawURL)
	if err != nil {
		return rawURL
	}
	host := parsed.Hostname()
	if host != "" && host != "duckduckgo.com" && !strings.HasSuffix(host, ".duckduckgo.com") {
		return rawURL
	}

	// Query already percent-decodes the value; do not unescape again or a
	// destination containing its own escapes would be mangled.
	decoded := parsed.Query().Get("uddg")
	if decoded == "" {
		return rawURL
	}
	return decoded
}

func formatSearchResults(results []SearchResult) string {
	if len(results) == 0 {
		return "No results found. Try rephrasing your search."
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "Found %d search results:\n\n", len(results))
	for _, result := range results {
		fmt.Fprintf(&sb, "%d. %s\n", result.Position, result.Title)
		fmt.Fprintf(&sb, "   URL: %s\n", result.Link)
		fmt.Fprintf(&sb, "   Summary: %s\n\n", result.Snippet)
	}
	return sb.String()
}

var (
	lastSearchMu   sync.Mutex
	lastSearchTime time.Time
)

// maybeDelaySearch adds a random delay if the last search was recent.
func maybeDelaySearch() {
	lastSearchMu.Lock()
	defer lastSearchMu.Unlock()

	minGap := time.Duration(500+rand.IntN(1500)) * time.Millisecond
	elapsed := time.Since(lastSearchTime)
	if elapsed < minGap {
		time.Sleep(minGap - elapsed)
	}
	lastSearchTime = time.Now()
}
