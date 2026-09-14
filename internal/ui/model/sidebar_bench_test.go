package model

import (
	"fmt"
	"testing"

	"charm.land/catwalk/pkg/catwalk"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/history"
	"github.com/charmbracelet/crush/internal/lsp"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/workspace"
	uv "github.com/charmbracelet/ultraviolet"
)

// buildSidebarBenchUI returns a chat UI with a populated sidebar so the
// per-frame sidebar rebuild in Draw is measured with realistic inputs.
func buildSidebarBenchUI(tb testing.TB) *UI {
	tb.Helper()
	u := newFrameTestUI(tb)
	u.com.Workspace = &testWorkspace{cfg: &config.Config{
		Options:   &config.Options{},
		MCP:       config.MCPs{"server-a": {}, "server-b": {}},
		Providers: csync.NewMap[string, config.ProviderConfig](),
	}}
	u.agentReady = true
	u.agentModel = workspace.AgentModel{
		CatwalkCfg: catwalk.Model{
			ID:                     "gpt-5",
			Name:                   "GPT-5",
			ContextWindow:          400000,
			CanReason:              true,
			ReasoningLevels:        []string{"low", "medium", "high"},
			DefaultReasoningEffort: "medium",
		},
		ModelCfg: config.SelectedModel{
			Provider:        "openai",
			Model:           "gpt-5",
			ReasoningEffort: "high",
		},
	}
	u.session = &session.Session{
		ID:               "sidebar-bench",
		Title:            "Optimize the sidebar rendering path",
		PromptTokens:     12400,
		CompletionTokens: 3210,
		Cost:             0.42,
		Totals: session.SessionTokens{
			InputTokens:     9000,
			OutputTokens:    3210,
			CacheReadTokens: 3400,
		},
	}
	u.lspStates = map[string]workspace.LSPClientInfo{
		"gopls":  {Name: "gopls", State: lsp.StateReady, DiagnosticCount: 3},
		"govet":  {Name: "govet", State: lsp.StateError, Error: fmt.Errorf("crashed")},
		"gofmt":  {Name: "gofmt", State: lsp.StateStopped},
		"static": {Name: "static", State: lsp.StateStarting},
	}
	u.lspDiagnostics = map[string]lsp.DiagnosticCounts{
		"gopls": {Error: 1, Warning: 2, Hint: 3, Information: 4},
	}
	u.mcpStates = map[string]mcp.ClientInfo{
		"server-a": {Name: "server-a", State: mcp.StateConnected, Counts: mcp.Counts{Tools: 4, Prompts: 2, Resources: 1}},
		"server-b": {Name: "server-b", State: mcp.StateError, Error: fmt.Errorf("boom")},
	}
	u.skillStates = []*skills.SkillState{
		{Name: "alpha", Path: "/tmp/skills/alpha/SKILL.md", State: skills.StateNormal, UserInvocable: true, DisableModelInvocation: true},
		{Name: "beta", Path: "/tmp/skills/beta/SKILL.md", State: skills.StateNormal, DisableModelInvocation: true},
		{Name: "broken", Path: "/tmp/skills/broken/SKILL.md", State: skills.StateError, Err: fmt.Errorf("bad frontmatter")},
	}
	u.sessionFiles = []SessionFile{
		{FirstVersion: history.File{Path: "/tmp/crush-test/main.go"}, Additions: 12, Deletions: 3},
		{FirstVersion: history.File{Path: "/tmp/crush-test/ui.go"}, Additions: 4, Deletions: 0},
	}
	u.updateLayoutAndSize()
	return u
}

// BenchmarkSidebar_Update measures the sidebar rebuild Draw performs every
// frame. When nothing changed, the memoized path should be dominated by key
// hashing instead of lipgloss rendering.
func BenchmarkSidebar_Update(b *testing.B) {
	u := buildSidebarBenchUI(b)
	u.updateSidebarScrollState() // warm the caches
	b.ReportAllocs()
	for b.Loop() {
		u.updateSidebarScrollState()
	}
}

// BenchmarkSidebar_UpdateStreaming measures the worst case while streaming:
// session token counters change on every frame, so the model info section is
// re-rendered while every other section stays memoized.
func BenchmarkSidebar_UpdateStreaming(b *testing.B) {
	u := buildSidebarBenchUI(b)
	u.updateSidebarScrollState() // warm the caches
	b.ReportAllocs()
	i := 0
	for b.Loop() {
		i++
		u.session.CompletionTokens = int64(3210 + i)
		u.updateSidebarScrollState()
	}
}

// BenchmarkSidebar_Draw measures drawing the visible sidebar content into a
// screen buffer, which only changes on scroll or content changes.
func BenchmarkSidebar_Draw(b *testing.B) {
	u := buildSidebarBenchUI(b)
	scr := uv.NewScreenBuffer(32, 40)
	area := uv.Rect(0, 0, 32, 40)
	u.updateSidebarScrollState()
	b.ReportAllocs()
	for b.Loop() {
		u.drawSidebar(scr, area)
	}
}

// BenchmarkView_SidebarUncached measures a full uncached frame with the
// sidebar visible, exercising View's screen buffer plus normalization.
func BenchmarkView_SidebarUncached(b *testing.B) {
	u := buildSidebarBenchUI(b)
	u.frames = nil
	b.ReportAllocs()
	for b.Loop() {
		u.beginFrameUpdate()
		u.View()
	}
}
