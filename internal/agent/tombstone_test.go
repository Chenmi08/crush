package agent

import (
	"strings"
	"testing"
	"unicode/utf8"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/stretchr/testify/require"
)

// bashToolResult builds a result with a body large enough that the policy
// considers it worth eliding.
func bashToolResult(id, content string) message.ToolResult {
	return message.ToolResult{
		ToolCallID: id,
		Name:       tools.BashToolName,
		Content:    content,
	}
}

// longOutput is a plausible command result: a header line, a failure marker
// and enough filler to be larger than the keep threshold.
func longOutput() string {
	return "$ go test ./internal/agent/...\n" +
		"ok  \tgithub.com/charmbracelet/crush/internal/agent\t0.021s\n" +
		strings.Repeat("some ordinary log line that is not interesting\n", 100) +
		"--- FAIL: TestSummarize (0.31s)\n" +
		"FAIL\n\nExit code 1"
}

func TestRenderTombstone_HeaderFieldOrderAndFooter(t *testing.T) {
	t.Parallel()

	r := bashToolResult("call_7f3a", longOutput())
	got := renderTombstone(r, 1761222724)

	header, rest, ok := strings.Cut(got, "\n")
	require.True(t, ok)
	require.Equal(
		t,
		"[elided] tool_call_id=call_7f3a name=bash exit=1 bytes=4838 at=2025-10-23T12:32:04Z",
		header,
	)
	require.Contains(t, rest, "  $ go test ./internal/agent/...")
	require.Contains(t, rest, "  --- FAIL: TestSummarize (0.31s)")
	require.Contains(t, got, `... (104 more lines) → recall(tool_call_id="call_7f3a")`)
}

func TestRenderTombstone_ExitOmittedForNonBashTools(t *testing.T) {
	t.Parallel()

	// A view whose tail quotes a status line must not be reported as a
	// failure: only bash results carry the status.
	r := message.ToolResult{
		ToolCallID: "call_view",
		Name:       tools.ViewToolName,
		Content:    strings.Repeat("x", 4096) + "\n\nExit code 3",
	}
	got := renderTombstone(r, 0)
	require.Contains(t, got, "name=view")
	require.NotContains(t, got, "exit=")
}

func TestRenderTombstone_PreviewsAreDeduped(t *testing.T) {
	t.Parallel()

	// Only one non-empty line, which is also the failure line.
	r := bashToolResult("call_x", "--- FAIL: TestNothing (0.1s)\n")
	got := renderTombstone(r, 0)
	require.Equal(t, 1, strings.Count(got, "--- FAIL: TestNothing (0.1s)"))
	require.NotContains(t, got, "more lines")
}

func TestRenderTombstone_FitsBudget(t *testing.T) {
	t.Parallel()

	// Header, previews and footer all present: the result must stay under
	// the cap, which means the previews are dropped.
	r := bashToolResult("call_long_"+strings.Repeat("i", 120), longOutput())
	got := renderTombstone(r, 0)
	require.LessOrEqual(t, len(got), tombstoneMaxBytes)
	require.Contains(t, got, "[elided] tool_call_id=call_long_")
	require.Contains(t, got, "recall(tool_call_id=")
	require.NotContains(t, got, "  $ go test", "previews are the first thing dropped")
}

func TestRenderTombstone_ClipsPreviewOnRuneBoundary(t *testing.T) {
	t.Parallel()

	r := bashToolResult("call_utf8", strings.Repeat("é", 400))
	got := renderTombstone(r, 0)
	require.Contains(t, got, "...")
	require.True(t, utf8.ValidString(got), "clipping must not split a multi-byte rune")
}

func TestShouldElide(t *testing.T) {
	t.Parallel()

	big := strings.Repeat("a", elideKeepBytes+1)
	small := strings.Repeat("a", elideKeepBytes)

	results := []message.ToolResult{
		bashToolResult("old", big),
		bashToolResult("read", big),
		{ToolCallID: "view", Name: tools.ViewToolName, Content: big},
		{ToolCallID: "media", Name: "fetch", Content: "ignored", Data: "aGVsbG8="},
		bashToolResult("small", small),
		bashToolResult("recent1", big),
		bashToolResult("recent2", big),
		bashToolResult("recent3", big),
	}
	results[1].Name = tools.ViewToolName

	policy := defaultElidePolicy()
	policy.Mode = elideOn

	tests := []struct {
		idx  int
		want bool
		why  string
	}{
		{idx: 0, want: true, why: "an old, large, non-exempt result"},
		{idx: 1, want: false, why: "view results are exempt"},
		{idx: 2, want: false, why: "view results are exempt"},
		{idx: 3, want: false, why: "media payloads cannot be recalled"},
		{idx: 4, want: false, why: "small results are kept"},
		{idx: 5, want: false, why: "within KeepRecent"},
	}
	for _, tt := range tests {
		require.Equal(t, tt.want, shouldElide(results, tt.idx, policy, nil), tt.why)
	}

	// Every mode other than on refuses.
	for _, mode := range []elideMode{elideOff, elideShadow} {
		p := defaultElidePolicy()
		p.Mode = mode
		if mode == elideOff {
			require.False(t, shouldElide(results, 0, p, nil))
		} else {
			require.True(t, shouldElide(results, 0, p, nil), "shadow mode computes the same decision")
		}
	}

	// A result from the current turn is never elided.
	require.False(t, shouldElide(results, 0, policy, map[string]struct{}{"old": {}}))
}

func TestElideModeFromOption(t *testing.T) {
	t.Parallel()

	tests := []struct {
		in   config.CompactionElideMode
		mode elideMode
	}{
		{in: "", mode: elideShadow},
		{in: config.CompactionElideShadow, mode: elideShadow},
		{in: config.CompactionElideOff, mode: elideOff},
		{in: config.CompactionElideOn, mode: elideOn},
		{in: "bogus", mode: elideShadow},
	}
	for _, tt := range tests {
		require.Equal(t, tt.mode, elideModeFromOption(tt.in), tt.in)
	}
}
