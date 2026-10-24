package tools

import (
	"strings"
	"testing"

	"github.com/stretchr/testify/require"
)

// A test log may quote the exit-code phrase; only the trailing line counts.
func TestParseExitCodeIgnoresQuotedLines(t *testing.T) {
	t.Parallel()

	code, ok := ParseExitCode("test says:\nExit code 7\nbut the real run succeeded\n")
	require.True(t, ok)
	require.Equal(t, 7, code)

	code, ok = ParseExitCode("oops\n\nExit code 2")
	require.True(t, ok)
	require.Equal(t, 2, code)

	_, ok = ParseExitCode("no status line here")
	require.False(t, ok)

	_, ok = ParseExitCode("")
	require.False(t, ok)
}

// Only the tail is scanned, so a phrase buried earlier cannot be mistaken for
// the real status line.
func TestParseExitCodeIgnoresDistantQuote(t *testing.T) {
	t.Parallel()

	content := "Exit code 99\n" + strings.Repeat("noise\n", 200) + "\nExit code 3"
	code, ok := ParseExitCode(content)
	require.True(t, ok)
	require.Equal(t, 3, code)

	// Beyond the scan window the phrase is invisible.
	farBehind := "Exit code 99\n" + strings.Repeat("noise\n", 200)
	_, ok = ParseExitCode(farBehind)
	require.False(t, ok)
}
