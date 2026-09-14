package model

import (
	"cmp"
	"fmt"
	"image"
	"math"
	"strings"

	"charm.land/lipgloss/v2"
	mcp "github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/logo"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/charmbracelet/ultraviolet/layout"
	"github.com/charmbracelet/x/ansi"
)

// modelInfo renders the current model information including reasoning
// settings and context usage/cost for the sidebar.
func (m *UI) modelInfo(width int) string {
	model := m.selectedLargeModel()
	reasoningInfo := ""
	providerName := ""

	if model != nil {
		// Get provider name first
		providerConfig, ok := m.com.Config().Providers.Get(model.ModelCfg.Provider)
		if ok {
			providerName = providerConfig.Name

			// Only check reasoning if model can reason
			if model.CatwalkCfg.CanReason {
				if len(model.CatwalkCfg.ReasoningLevels) == 0 {
					if model.ModelCfg.Think {
						reasoningInfo = "Thinking On"
					} else {
						reasoningInfo = "Thinking Off"
					}
				} else {
					reasoningEffort := cmp.Or(model.ModelCfg.ReasoningEffort, model.CatwalkCfg.DefaultReasoningEffort)
					reasoningInfo = fmt.Sprintf("Reasoning %s", common.FormatReasoningEffort(reasoningEffort))
				}
			}
		}
	}

	var modelContext *common.ModelContextInfo
	if model != nil && m.session != nil {
		modelContext = &common.ModelContextInfo{
			ContextUsed:    m.session.CompletionTokens + m.session.PromptTokens,
			Cost:           m.session.Cost,
			ModelContext:   model.CatwalkCfg.ContextWindow,
			EstimatedUsage: m.session.EstimatedUsage,
			Totals:         m.session.Totals,
		}
	}
	var modelName string
	if model != nil {
		modelName = model.CatwalkCfg.Name
	}
	return common.ModelInfo(m.com.Styles, modelName, providerName, reasoningInfo, modelContext, width, m.hyperCredits)
}

// The sidebar is rebuilt on every frame, but its inputs change far less
// often than that. Each section is memoized by an FNV-1a fingerprint of the
// exact values it renders from, so the expensive lipgloss/text-width work
// only runs when something visible changed.
const (
	sidebarHashSeed  uint64 = 14695981039346656037
	sidebarHashPrime uint64 = 1099511628211
)

func sidebarHashString(h uint64, s string) uint64 {
	for i := 0; i < len(s); i++ {
		h ^= uint64(s[i])
		h *= sidebarHashPrime
	}
	return h
}

func sidebarHashUint64(h uint64, v uint64) uint64 {
	h ^= v
	h *= sidebarHashPrime
	return h
}

func sidebarHashInt(h uint64, v int) uint64 {
	return sidebarHashUint64(h, uint64(int64(v)))
}

func sidebarHashInt64(h uint64, v int64) uint64 {
	return sidebarHashUint64(h, uint64(v))
}

func sidebarHashBool(h uint64, v bool) uint64 {
	if v {
		return sidebarHashUint64(h, 1)
	}
	return sidebarHashUint64(h, 0)
}

func sidebarHashFloat(h uint64, v float64) uint64 {
	return sidebarHashUint64(h, math.Float64bits(v))
}

func sidebarHashError(h uint64, err error) uint64 {
	if err == nil {
		return sidebarHashString(h, "\x00nil")
	}
	return sidebarHashString(h, err.Error())
}

// cachedSidebarSection memoizes one rendered sidebar section by input key.
type cachedSidebarSection struct {
	key   uint64
	valid bool
	value string
}

func (c *cachedSidebarSection) get(key uint64, render func() string) string {
	if c.valid && c.key == key {
		return c.value
	}
	c.value = render()
	c.key = key
	c.valid = true
	return c.value
}

// sidebarSectionCache memoizes the rendered sidebar sections plus the
// assembled content. The contentKey fast path skips the assembly entirely
// when every section input is unchanged.
type sidebarSectionCache struct {
	contentKey    uint64
	contentOK     bool
	content       string
	totalLines    int
	contentHeight int
	drawLogo      string

	title  cachedSidebarSection
	cwd    cachedSidebarSection
	logo   cachedSidebarSection
	model  cachedSidebarSection
	lsp    cachedSidebarSection
	mcp    cachedSidebarSection
	skills cachedSidebarSection
	files  cachedSidebarSection
}

// sidebarDrawCache caches the ANSI-decoded visible sidebar content so a
// redraw of an unchanged region is a cell copy instead of a re-parse, the
// same trick the chat list uses for its draw cache.
type sidebarDrawCache struct {
	key   uint64
	valid bool
	buf   uv.ScreenBuffer
}

// update re-renders the visible lines into the cache buffer, resizing the
// buffer in place when the region dimensions changed.
func (c *sidebarDrawCache) update(key uint64, lines []string, from, to, width, height int, method ansi.Method) {
	visible := strings.Join(lines[from:to], "\n")
	styled := lipgloss.NewStyle().MaxWidth(width).MaxHeight(height).Render(visible)
	w, h := max(width, 1), max(height, 1)
	if c.buf.RenderBuffer == nil {
		c.buf = uv.NewScreenBuffer(w, h)
	} else {
		c.buf.Resize(w, h)
	}
	c.buf.Method = method
	// StyledString.Draw clears the destination area first, so the reused
	// buffer never leaks cells from the previous render.
	uv.NewStyledString(styled).Draw(c.buf, c.buf.Bounds())
	c.key = key
	c.valid = true
}

// invalidateSidebar drops every memoized sidebar rendering. Call it when an
// input that isn't captured by the section keys changes (e.g. the theme).
func (m *UI) invalidateSidebar() {
	m.sidebarSections = sidebarSectionCache{}
	m.sidebarDraw = sidebarDrawCache{}
	m.sidebarLines = nil
	m.sidebarContentVersion++
}

// sidebarModelInfoKey fingerprints the model and session inputs rendered by
// modelInfo. Unlike the other sections these change while a turn streams,
// which is exactly when the sidebar must update.
func (m *UI) sidebarModelInfoKey(width int) uint64 {
	h := sidebarHashInt(sidebarHashSeed, width)
	if model := m.selectedLargeModel(); model != nil {
		h = sidebarHashString(h, model.ModelCfg.Provider)
		h = sidebarHashString(h, model.ModelCfg.Model)
		h = sidebarHashString(h, model.ModelCfg.ReasoningEffort)
		h = sidebarHashBool(h, model.ModelCfg.Think)
		h = sidebarHashString(h, model.CatwalkCfg.Name)
		h = sidebarHashBool(h, model.CatwalkCfg.CanReason)
		h = sidebarHashInt64(h, model.CatwalkCfg.ContextWindow)
		h = sidebarHashString(h, model.CatwalkCfg.DefaultReasoningEffort)
		for _, level := range model.CatwalkCfg.ReasoningLevels {
			h = sidebarHashString(h, level)
		}
		if provider, ok := m.com.Config().Providers.Get(model.ModelCfg.Provider); ok {
			h = sidebarHashString(h, provider.Name)
		}
	} else {
		h = sidebarHashString(h, "\x00no-model")
	}
	if s := m.session; s != nil {
		h = sidebarHashInt64(h, s.CompletionTokens)
		h = sidebarHashInt64(h, s.PromptTokens)
		h = sidebarHashFloat(h, s.Cost)
		h = sidebarHashBool(h, s.EstimatedUsage)
		h = sidebarHashInt64(h, s.Totals.InputTokens)
		h = sidebarHashInt64(h, s.Totals.OutputTokens)
		h = sidebarHashInt64(h, s.Totals.CacheReadTokens)
		h = sidebarHashInt64(h, s.Totals.CacheCreationTokens)
	}
	if m.hyperCredits == nil {
		h = sidebarHashString(h, "\x00no-credits")
	} else {
		h = sidebarHashInt(h, *m.hyperCredits)
	}
	return h
}

// sidebarLSPKey fingerprints the LSP states and diagnostics. Map iteration
// order is random, so entries are combined commutatively; lspInfo renders
// them sorted by name.
func (m *UI) sidebarLSPKey(width int) uint64 {
	var states uint64
	for name, state := range m.lspStates {
		entry := sidebarHashString(sidebarHashSeed, name)
		entry = sidebarHashString(entry, state.Name)
		entry = sidebarHashInt(entry, int(state.State))
		entry = sidebarHashError(entry, state.Error)
		entry = sidebarHashInt(entry, state.DiagnosticCount)
		states += entry
	}
	var diagnostics uint64
	for name, counts := range m.lspDiagnostics {
		entry := sidebarHashString(sidebarHashSeed, name)
		entry = sidebarHashInt(entry, counts.Error)
		entry = sidebarHashInt(entry, counts.Warning)
		entry = sidebarHashInt(entry, counts.Hint)
		entry = sidebarHashInt(entry, counts.Information)
		diagnostics += entry
	}
	h := sidebarHashInt(sidebarHashSeed, width)
	return h ^ states ^ diagnostics
}

// sidebarMCPKey fingerprints the configured MCP servers and their states.
// mcpInfo renders the servers sorted by name, so entries are combined
// commutatively and iterating the config map directly avoids allocating a
// sorted copy on every frame.
func (m *UI) sidebarMCPKey(width int) uint64 {
	h := sidebarHashInt(sidebarHashSeed, width)
	var entries uint64
	for name := range m.com.Config().MCP {
		entry := sidebarHashString(sidebarHashSeed, name)
		state, ok := m.mcpStates[name]
		if !ok {
			entry = sidebarHashString(entry, "\x00absent")
		} else {
			entry = sidebarHashString(entry, state.Name)
			entry = sidebarHashInt(entry, int(state.State))
			entry = sidebarHashError(entry, state.Error)
			entry = sidebarHashInt(entry, state.Counts.Tools)
			entry = sidebarHashInt(entry, state.Counts.Prompts)
			entry = sidebarHashInt(entry, state.Counts.Resources)
		}
		entries += entry
	}
	return h ^ entries
}

// sidebarSkillsKey fingerprints the discovered skill states and the
// disabled-skills config. skillStatusItems orders states by path, so they
// are hashed in order; the disabled set is order-independent.
func (m *UI) sidebarSkillsKey(width int) uint64 {
	h := sidebarHashInt(sidebarHashSeed, width)
	for _, state := range m.skillStates {
		if state == nil {
			continue
		}
		h = sidebarHashString(h, state.Name)
		h = sidebarHashString(h, state.Path)
		h = sidebarHashInt(h, int(state.State))
		h = sidebarHashError(h, state.Err)
		h = sidebarHashBool(h, state.UserInvocable)
		h = sidebarHashBool(h, state.DisableModelInvocation)
	}
	if m.com != nil {
		if cfg := m.com.Config(); cfg != nil && cfg.Options != nil {
			var disabled uint64
			for _, name := range cfg.Options.DisabledSkills {
				disabled += sidebarHashString(sidebarHashSeed, name)
			}
			h ^= disabled
		}
	}
	return h
}

// sidebarFilesKey fingerprints the modified-files section inputs.
func (m *UI) sidebarFilesKey(width int, cwd string) uint64 {
	h := sidebarHashString(sidebarHashInt(sidebarHashSeed, width), cwd)
	for _, f := range m.sessionFiles {
		h = sidebarHashString(h, f.FirstVersion.Path)
		h = sidebarHashInt(h, f.Additions)
		h = sidebarHashInt(h, f.Deletions)
	}
	return h
}

// updateSidebarScrollState renders the sidebar content and computes scroll
// state (scrollability, max offset, clamp) before drawing. This keeps all
// state mutation in the update path rather than in the draw function.
func (m *UI) updateSidebarScrollState() {
	if m.session == nil || m.isCompact {
		return
	}

	const logoHeightBreakpoint = 30

	t := m.com.Styles
	width := m.layout.sidebar.Dx()
	height := m.layout.sidebar.Dy()

	contentWidth := max(width-2, 1)

	cache := &m.sidebarSections
	titleKey := sidebarHashString(sidebarHashInt(sidebarHashSeed, contentWidth), m.session.Title)
	cwd := m.com.Workspace.WorkingDir()
	cwdKey := sidebarHashString(sidebarHashInt(sidebarHashSeed, contentWidth), cwd)
	logoKey := sidebarHashBool(sidebarHashInt(sidebarHashSeed, contentWidth), height < logoHeightBreakpoint)
	logoKey = sidebarHashBool(logoKey, m.com.IsHyper())
	modelKey := m.sidebarModelInfoKey(contentWidth)
	lspKey := m.sidebarLSPKey(contentWidth)
	mcpKey := m.sidebarMCPKey(contentWidth)
	skillsKey := m.sidebarSkillsKey(contentWidth)
	filesKey := m.sidebarFilesKey(contentWidth, cwd)

	fullKey := sidebarHashUint64(sidebarHashSeed, titleKey)
	fullKey = sidebarHashUint64(fullKey, cwdKey)
	fullKey = sidebarHashUint64(fullKey, logoKey)
	fullKey = sidebarHashUint64(fullKey, modelKey)
	fullKey = sidebarHashUint64(fullKey, lspKey)
	fullKey = sidebarHashUint64(fullKey, mcpKey)
	fullKey = sidebarHashUint64(fullKey, skillsKey)
	fullKey = sidebarHashUint64(fullKey, filesKey)
	fullKey = sidebarHashInt(fullKey, width)
	fullKey = sidebarHashInt(fullKey, height)

	var (
		sidebarLogo   string
		content       string
		totalLines    int
		contentHeight int
	)
	if cache.contentOK && cache.contentKey == fullKey {
		sidebarLogo = cache.drawLogo
		content = cache.content
		totalLines = cache.totalLines
		contentHeight = cache.contentHeight
	} else {
		title := cache.title.get(titleKey, func() string {
			return t.Sidebar.SessionTitle.Width(contentWidth).MaxHeight(2).Render(m.session.Title)
		})
		cwdLine := cache.cwd.get(cwdKey, func() string {
			return common.PrettyPath(t, cwd, contentWidth)
		})
		sidebarLogo = m.sidebarLogo
		if height < logoHeightBreakpoint {
			sidebarLogo = cache.logo.get(logoKey, func() string {
				return lipgloss.JoinVertical(lipgloss.Left, logo.SmallRender(t, contentWidth, logo.Opts{
					Hyper: m.com.IsHyper(),
				}), "")
			})
		}

		// Render all items without truncation; virtual scrolling handles
		// overflow.
		lspSection := cache.lsp.get(lspKey, func() string {
			return m.lspInfo(contentWidth, len(m.lspStates), true)
		})
		mcpSection := cache.mcp.get(mcpKey, func() string {
			return m.mcpInfo(contentWidth, mcpCount(m.com.Config().MCP.Sorted(), m.mcpStates), true)
		})
		skillsSection := cache.skills.get(skillsKey, func() string {
			return m.skillsInfo(contentWidth, len(m.skillStatusItems()), true)
		})
		filesSection := cache.files.get(filesKey, func() string {
			return m.filesInfo(cwd, contentWidth, fileChangeCount(m.sessionFiles), true)
		})
		modelSection := cache.model.get(modelKey, func() string {
			return m.modelInfo(contentWidth)
		})

		// Build the scrollable content.
		content = lipgloss.JoinVertical(
			lipgloss.Left,
			title,
			"",
			cwdLine,
			"",
			modelSection,
			"",
			filesSection,
			"",
			lspSection,
			"",
			mcpSection,
			"",
			skillsSection,
		)
		totalLines = strings.Count(content, "\n") + 1

		var logoRect, contentRect image.Rectangle
		layout.Vertical(
			layout.Len(lipgloss.Height(sidebarLogo)),
			layout.Fill(1),
		).Split(m.layout.sidebar).Assign(&logoRect, &contentRect)
		contentHeight = contentRect.Dy()

		cache.contentKey = fullKey
		cache.contentOK = true
		cache.content = content
		cache.totalLines = totalLines
		cache.contentHeight = contentHeight
		cache.drawLogo = sidebarLogo
		m.sidebarLines = strings.Split(content, "\n")
		m.sidebarContentVersion++
	}

	m.sidebarContent = content
	m.sidebarTotalLines = totalLines
	m.sidebarContentWidth = contentWidth
	m.sidebarContentHeight = contentHeight
	m.sidebarDrawLogo = sidebarLogo
	m.sidebarScrollable = totalLines > m.sidebarContentHeight
	m.sidebarMaxOffsetVal = max(0, totalLines-m.sidebarContentHeight)

	// If the sidebar is focused but no longer scrollable (e.g. after a
	// resize), return focus to the chat.
	if m.focus == uiFocusSidebar && !m.sidebarScrollable {
		m.focus = uiFocusMain
		m.chat.Focus()
	}

	// Clamp sidebarOffset.
	if m.sidebarOffset > m.sidebarMaxOffsetVal {
		m.sidebarOffset = m.sidebarMaxOffsetVal
	}
}

// drawSidebar renders the chat sidebar with a fixed logo and a
// virtual-scrolling content area with an auto-hiding scrollbar. While the
// sidebar is focused, the scrollbar stays visible.
func (m *UI) drawSidebar(scr uv.Screen, area uv.Rectangle) {
	if m.session == nil {
		return
	}

	sidebarLogo := m.sidebarDrawLogo
	contentWidth := m.sidebarContentWidth
	contentHeight := m.sidebarContentHeight
	totalLines := m.sidebarTotalLines

	var logoRect, contentRect image.Rectangle
	layout.Vertical(
		layout.Len(lipgloss.Height(sidebarLogo)),
		layout.Fill(1),
	).Split(area).Assign(&logoRect, &contentRect)

	// Determine scrollbar visibility: always visible when focused, otherwise
	// auto-hide.
	scrollbarVisible := totalLines > contentHeight && (m.sidebarScrollbarVisible || m.focus == uiFocusSidebar)

	// Draw the fixed logo.
	uv.NewStyledString(
		lipgloss.NewStyle().
			MaxWidth(contentWidth).
			MaxHeight(lipgloss.Height(sidebarLogo)).
			Render(sidebarLogo),
	).Draw(scr, logoRect)

	// Draw the visible content in the scrollable area, re-decoding it only
	// when the visible region changed.
	end := min(m.sidebarOffset+contentHeight, totalLines)
	method, ok := scr.WidthMethod().(ansi.Method)
	if !ok || m.sidebarLines == nil {
		// Fall back to the uncached path when the width method isn't an
		// ansi.Method (unlikely) or the line cache is missing.
		lines := m.sidebarLines
		if lines == nil {
			lines = strings.Split(m.sidebarContent, "\n")
		}
		visibleStr := strings.Join(lines[m.sidebarOffset:end], "\n")
		uv.NewStyledString(
			lipgloss.NewStyle().
				MaxWidth(contentWidth).
				MaxHeight(contentHeight).
				Render(visibleStr),
		).Draw(scr, contentRect)
	} else {
		key := sidebarHashInt(sidebarHashSeed, m.sidebarContentVersion)
		key = sidebarHashInt(key, m.sidebarOffset)
		key = sidebarHashInt(key, end)
		key = sidebarHashInt(key, contentWidth)
		key = sidebarHashInt(key, contentHeight)
		if !m.sidebarDraw.valid || m.sidebarDraw.key != key {
			m.sidebarDraw.update(key, m.sidebarLines, m.sidebarOffset, end, contentWidth, contentHeight, method)
		}
		drawCachedBuffer(scr, contentRect, m.sidebarDraw.buf)
	}

	// Draw scrollbar in the reserved column.
	if scrollbarVisible {
		scrollbar := common.Scrollbar(m.com.Styles, contentHeight, totalLines, contentHeight, m.sidebarOffset)
		if scrollbar != "" {
			scrollbarArea := image.Rectangle{
				Min: image.Point{X: area.Max.X - 1, Y: contentRect.Min.Y},
				Max: image.Point{X: area.Max.X, Y: area.Max.Y},
			}
			uv.NewStyledString(scrollbar).Draw(scr, scrollbarArea)
		}
	}
}

// fileChangeCount returns the number of session files with non-zero additions
// or deletions.
func fileChangeCount(files []SessionFile) int {
	count := 0
	for _, f := range files {
		if f.Additions == 0 && f.Deletions == 0 {
			continue
		}
		count++
	}
	return count
}

// mcpCount returns the number of MCP servers that have a state entry.
func mcpCount(mcpCfgs []config.MCP, states map[string]mcp.ClientInfo) int {
	count := 0
	for _, cfg := range mcpCfgs {
		if _, ok := states[cfg.Name]; ok {
			count++
		}
	}
	return count
}
