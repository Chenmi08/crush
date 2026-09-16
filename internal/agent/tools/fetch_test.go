package tools

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/stretchr/testify/require"
)

// TestTruncateFetchContent verifies the truncation notice is emitted
// whenever bytes were dropped, even if format conversion shrank the content
// below the limit (the previously silent case), and not when nothing was cut.
func TestTruncateFetchContent(t *testing.T) {
	t.Parallel()

	const notice = "\n\n[Content truncated to 102400 bytes]"

	short := strings.Repeat("a", 10)
	got, truncated := truncateFetchContent(short, false)
	require.False(t, truncated)
	require.Equal(t, short, got)

	// Conversion shrank the payload, but the source was cut: still report it.
	got, truncated = truncateFetchContent(short, true)
	require.True(t, truncated)
	require.Equal(t, short+notice, got)

	// Converted content that grew past the cap is cut and reported.
	long := strings.Repeat("a", MaxFetchSize+50)
	got, truncated = truncateFetchContent(long, false)
	require.True(t, truncated)
	require.True(t, strings.HasSuffix(got, notice), "missing truncation notice: %q", got)
	require.Len(t, strings.TrimSuffix(got, notice), MaxFetchSize)

	// Exactly at the cap with no source truncation lost no bytes.
	exact := strings.Repeat("a", MaxFetchSize)
	got, truncated = truncateFetchContent(exact, false)
	require.False(t, truncated)
	require.Equal(t, exact, got)
}

// TestFetchURLAndConvertTruncatesLargeContent verifies oversized responses
// are capped rune-safely and marked with a notice.
func TestFetchURLAndConvertTruncatesLargeContent(t *testing.T) {
	orig := maxFetchConvertBytes
	maxFetchConvertBytes = 32
	t.Cleanup(func() { maxFetchConvertBytes = orig })

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte(strings.Repeat("a", 100)))
	}))
	t.Cleanup(srv.Close)

	content, err := FetchURLAndConvert(context.Background(), srv.Client(), srv.URL)
	require.NoError(t, err)

	const notice = "\n\n[Content truncated to 32 bytes]"
	require.True(t, strings.HasSuffix(content, notice), "missing truncation notice: %q", content)
	require.True(t, utf8.ValidString(content))
	require.Equal(t, 32, len(strings.TrimSuffix(content, notice)))
}

// TestFetchURLAndConvertSmallContentUntouched verifies the notice is not
// added to content that fits.
func TestFetchURLAndConvertSmallContentUntouched(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("hello"))
	}))
	t.Cleanup(srv.Close)

	content, err := FetchURLAndConvert(context.Background(), srv.Client(), srv.URL)
	require.NoError(t, err)
	require.Equal(t, "hello", content)
}

// TestFetchURLAndConvertTruncatesMultiByteSafely guards against cutting a
// rune in half at the size limit, which would fail UTF-8 validation.
func TestFetchURLAndConvertTruncatesMultiByteSafely(t *testing.T) {
	orig := maxFetchConvertBytes
	maxFetchConvertBytes = 4
	t.Cleanup(func() { maxFetchConvertBytes = orig })

	// "é" is two bytes, so a 5-byte payload splits the third rune.
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		_, _ = w.Write([]byte("ééé"))
	}))
	t.Cleanup(srv.Close)

	content, err := FetchURLAndConvert(context.Background(), srv.Client(), srv.URL)
	require.NoError(t, err)
	require.True(t, utf8.ValidString(content), "content is not valid UTF-8: %q", content)
}
