package model

import (
	"errors"
	"testing"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/stretchr/testify/require"
)

// TestSidebarCache_InvalidatesOnInputChange verifies that the memoized
// sidebar content tracks every keyed input. Each case starts from a fresh
// UI, mutates one input, and requires the warm render to match a cold
// (cache-invalidated) render — a key that misses the mutation would leave
// stale content behind.
func TestSidebarCache_InvalidatesOnInputChange(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name   string
		mutate func(u *UI)
	}{
		{"session title", func(u *UI) { u.session.Title = "A completely different title" }},
		{"session tokens", func(u *UI) { u.session.CompletionTokens += 5000 }},
		{"session cost", func(u *UI) { u.session.Cost += 1.25 }},
		{"lsp state", func(u *UI) {
			u.lspStates["gopls"] = workspace.LSPClientInfo{Name: "gopls", State: lsp.StateError, Error: errors.New("crashed")}
		}},
		{"lsp diagnostics", func(u *UI) {
			u.lspDiagnostics["gopls"] = lsp.DiagnosticCounts{Error: 9}
		}},
		{"mcp state", func(u *UI) {
			u.mcpStates["server-a"] = mcp.ClientInfo{Name: "server-a", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 99}}
		}},
		{"skills", func(u *UI) {
			u.skillStates = append(u.skillStates, &skills.SkillState{
				Name: "gamma", Path: "/tmp/skills/gamma/SKILL.md", State: skills.StateNormal,
			})
		}},
		{"session files", func(u *UI) { u.sessionFiles[0].Additions = 999 }},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			u := buildSidebarBenchUI(t)
			u.updateSidebarScrollState()
			before := u.sidebarContent
			require.NotEmpty(t, before, "sidebar should render content")

			// Unchanged inputs must hit the memoized fast path.
			u.updateSidebarScrollState()
			require.Equal(t, before, u.sidebarContent, "unchanged sidebar must reuse cached content")

			tc.mutate(u)
			u.updateSidebarScrollState()
			warm := u.sidebarContent
			require.NotEqual(t, before, warm, "mutation must change the rendered sidebar")

			// A cold render must agree with the memoized one.
			u.invalidateSidebar()
			u.updateSidebarScrollState()
			require.Equal(t, warm, u.sidebarContent, "warm render must match cold render")
		})
	}
}

// TestSidebarDrawCache_ReuseMatchesFreshBuffer verifies that reusing and
// resizing the decoded draw buffer never leaks cells from a previous visible
// region, by comparing a reused buffer against a freshly allocated one.
func TestSidebarDrawCache_ReuseMatchesFreshBuffer(t *testing.T) {
	t.Parallel()

	u := buildSidebarBenchUI(t)
	// Shrink the viewport so the fixture overflows by more than the
	// scroll step below; the default test height fits the whole fixture.
	u.height = 32
	u.updateLayoutAndSize()
	u.sidebarOffset = 0
	u.updateSidebarScrollState()
	require.Greater(t, u.sidebarMaxOffsetVal, 2, "sidebar must be scrollable for this test")

	area := uv.Rect(0, 0, 32, 40)

	scr := uv.NewScreenBuffer(32, 40)
	u.drawSidebar(scr, area) // prime the draw cache

	// Scroll a few lines: the visible region changes, so the cache
	// re-decodes into the existing buffer.
	u.sidebarOffset = 3
	u.updateSidebarScrollState()
	u.drawSidebar(scr, area)

	// Reference: same state with an empty cache drawing into a fresh buffer.
	u.sidebarDraw = sidebarDrawCache{}
	ref := uv.NewScreenBuffer(32, 40)
	u.drawSidebar(ref, area)

	require.Equal(t, ref.Render(), scr.Render(),
		"reused sidebar draw buffer must match a fresh render")
}
