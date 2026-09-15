package model

import (
	"fmt"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
)

type skillStatusItem struct {
	icon string
	// name is truncated to fit the column at render time.
	name string
	// suffix is the invocation marker rendered directly after the name,
	// e.g. "[u+m]". It is empty for error states and skills that neither
	// the user nor the model can invoke.
	suffix string
	// display overrides the rendered text for synthetic rows such as the
	// "…and N more" hint. Real skills leave it empty so the renderer can
	// truncate name and suffix to the available width.
	display string
	// description is reserved for future use (e.g. showing error details).
	description string
	// disabled reports whether the skill is turned off for the session in
	// view. Disabled skills stay listed (with an unchecked box) so clicking
	// them re-enables them.
	disabled bool
}

// skillRow is one rendered row of the Skills section. Line is the row's
// 0-based offset from the top of the section and Height is how many terminal
// lines it spans, so a click anywhere on the row can be mapped back to Name.
// Synthetic rows such as "…and N more" are not emitted because they cannot
// be toggled.
type skillRow struct {
	Name   string
	Line   int
	Height int
}

var builtinSkillsCache struct {
	once   sync.Once
	skills []*skills.Skill
}

func cachedBuiltinSkills() []*skills.Skill {
	builtinSkillsCache.once.Do(func() {
		builtinSkillsCache.skills = skills.DiscoverBuiltin()
	})
	return builtinSkillsCache.skills
}

// skillsInfo renders the skill discovery status section showing loaded and
// invalid skills.
func (m *UI) skillsInfo(width, maxItems int, isSection bool) string {
	section, _ := m.skillsSection(width, maxItems, isSection)
	return section
}

// skillsSection renders the Skills section and returns one [skillRow] per
// real skill row, with offsets relative to the section's first line. The
// sidebar uses those offsets to turn a click into a toggle.
func (m *UI) skillsSection(width, maxItems int, isSection bool) (string, []skillRow) {
	t := m.com.Styles

	title := t.Resource.Heading.Render("Skills")
	if isSection {
		title = common.Section(t, title, width)
	}
	style := lipgloss.NewStyle().Width(width)
	titleText := style.Render(title)

	items := m.allSkillStatusItems()
	if len(items) == 0 {
		list := t.Resource.AdditionalText.Render("None")
		return lipgloss.JoinVertical(lipgloss.Left, titleText, "", list), nil
	}

	texts := skillRowTexts(t, items, width, maxItems)
	rows := make([]skillRow, 0, len(texts))
	parts := []string{titleText, ""}
	// The title occupies its rendered height and the list starts after one
	// blank separator line.
	line := lipgloss.Height(titleText) + 1
	for _, rowText := range texts {
		text := style.Render(rowText.text)
		height := lipgloss.Height(text)
		// Synthetic rows still consume the line budget, but only real
		// skills are clickable.
		if rowText.name != "" {
			rows = append(rows, skillRow{Name: rowText.name, Line: line, Height: height})
		}
		line += height
		parts = append(parts, text)
	}
	return strings.Join(parts, "\n"), rows
}

// skillRowText is a rendered Skills row before its width style is applied.
type skillRowText struct {
	name string
	text string
}

// skillRowTexts renders one row per skill, plus the truncation hint when
// maxItems forces some rows out. Each row is rendered independently so
// callers can measure it without re-deriving layout.
func skillRowTexts(t *styles.Styles, items []skillStatusItem, width, maxItems int) []skillRowText {
	if maxItems <= 0 {
		return nil
	}

	if len(items) > maxItems {
		// Clone so the synthetic hint cannot overwrite a real item in
		// the caller's slice.
		visible := slices.Clone(items[:maxItems-1])
		remaining := len(items) - (maxItems - 1)
		items = append(visible, skillStatusItem{
			display: t.Resource.AdditionalText.Render(fmt.Sprintf("…and %d more", remaining)),
		})
	}

	rows := make([]skillRowText, 0, len(items))
	for _, item := range items {
		rows = append(rows, skillRowText{
			name: item.name,
			text: common.Status(t, common.StatusOpts{
				Icon:        item.icon,
				Title:       item.title(t, width),
				Description: item.description,
			}, width),
		})
	}
	return rows
}

// skillInvocationSuffix labels how a skill can be triggered: "[u]" for
// user-only skills, "[m]" for model-only skills, and "[u+m]" when both
// paths are available. Skills marked neither way return "".
func skillInvocationSuffix(userInvocable, disableModelInvocation bool) string {
	switch {
	case userInvocable && disableModelInvocation:
		return "[u]"
	case userInvocable:
		return "[u+m]"
	case !disableModelInvocation:
		return "[m]"
	default:
		return ""
	}
}

// disabledSkillNames returns the set of skill names disabled for the
// session in view. With no session loaded (landing page) it reports the
// landing-page pending set once the user has touched it, and otherwise
// falls back to the global options.disabled_skills default that new
// sessions are seeded from. It is empty when no workspace is attached,
// which is the case in unit tests that exercise the renderer in isolation.
func (m *UI) disabledSkillNames() map[string]bool {
	names := make(map[string]bool)
	var disabled []string
	switch {
	case m.session != nil:
		disabled = m.session.DisabledSkills
	case m.pendingSkillsDirty:
		disabled = m.pendingDisabledSkills
	default:
		disabled = m.globalDisabledSkills()
	}
	for _, name := range disabled {
		names[name] = true
	}
	return names
}

// globalDisabledSkills returns options.disabled_skills from the live config,
// the default a new session is seeded from, or nil when config is
// unavailable.
func (m *UI) globalDisabledSkills() []string {
	if m.com == nil || m.com.Workspace == nil {
		return nil
	}
	return m.com.Config().DisabledSkills()
}

// setSkillDisabled returns names with the given skill added or removed. The
// result stays sorted and deduplicated so the in-memory value matches what
// the session service persists.
func setSkillDisabled(names []string, name string, disabled bool) []string {
	cleaned := slices.Clone(names)
	idx := slices.Index(cleaned, name)
	switch {
	case disabled && idx < 0:
		cleaned = append(cleaned, name)
	case !disabled && idx >= 0:
		cleaned = slices.Delete(cleaned, idx, idx+1)
	}
	slices.Sort(cleaned)
	return slices.Compact(cleaned)
}

// allSkillStatusItems returns every known skill, including disabled ones,
// so the sidebar can show and toggle them.
func (m *UI) allSkillStatusItems() []skillStatusItem {
	t := m.com.Styles
	var items []skillStatusItem
	stateNames := make(map[string]struct{}, len(m.skillStates))
	disabledSet := m.disabledSkillNames()

	states := slices.Clone(m.skillStates)
	slices.SortStableFunc(states, func(a, b *skills.SkillState) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, state := range states {
		name := state.Name
		if name == "" {
			name = filepath.Base(filepath.Dir(state.Path))
		}
		if _, exists := stateNames[name]; exists {
			continue
		}
		stateNames[name] = struct{}{}
		disabled := disabledSet[name]
		// A single circle carries both discovery state and the on/off
		// state: green when enabled, dim when off, red on a load error.
		icon := t.Resource.OnlineIcon.String()
		suffix := ""
		switch {
		case state.State == skills.StateError:
			icon = t.Resource.ErrorIcon.String()
		default:
			suffix = skillInvocationSuffix(state.UserInvocable, state.DisableModelInvocation)
			if disabled {
				icon = t.Resource.DisabledIcon.String()
			}
		}
		items = append(items, skillStatusItem{
			icon:     icon,
			name:     name,
			suffix:   suffix,
			disabled: disabled,
		})
	}

	builtin := slices.Clone(cachedBuiltinSkills())
	slices.SortStableFunc(builtin, func(a, b *skills.Skill) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, skill := range builtin {
		if _, ok := stateNames[skill.Name]; ok {
			continue
		}
		disabled := disabledSet[skill.Name]
		icon := t.Resource.OnlineIcon.String()
		if disabled {
			icon = t.Resource.DisabledIcon.String()
		}
		items = append(items, skillStatusItem{
			icon:     icon,
			name:     skill.Name,
			suffix:   skillInvocationSuffix(skill.UserInvocable, skill.DisableModelInvocation),
			disabled: disabled,
		})
	}

	sortSkillStatusItems(items)
	return items
}

// sortSkillStatusItems orders rows in two levels: by invocation marker
// first — "[m]", "[u+m]", "[u]" — and alphabetically within a group, so
// similar skills sit together. Skills with no marker (errors, or skills
// neither the user nor the model can invoke) sort last.
func sortSkillStatusItems(items []skillStatusItem) {
	slices.SortStableFunc(items, func(a, b skillStatusItem) int {
		if aEmpty, bEmpty := a.suffix == "", b.suffix == ""; aEmpty != bEmpty {
			if aEmpty {
				return 1
			}
			return -1
		}
		if c := strings.Compare(a.suffix, b.suffix); c != 0 {
			return c
		}
		return strings.Compare(a.name, b.name)
	})
}

// title renders a row title: the skill name followed by its invocation
// suffix. The enabled state is carried by the row's leading circle (see
// allSkillStatusItems), so no separate checkbox is drawn. The name is
// elided so the row fits in width instead of wrapping, keeping one
// rendered line per skill (the sidebar relies on that to map clicks to
// rows). The suffix stays visible; only the name truncates.
func (item skillStatusItem) title(t *styles.Styles, width int) string {
	if item.display != "" {
		return item.display
	}

	iconWidth := lipgloss.Width(item.icon)
	nameStyle := t.Resource.Name
	if item.disabled {
		nameStyle = t.Resource.AdditionalText
	}
	suffix := item.suffix

	nameWidth := width - iconWidth - 1 - lipgloss.Width(suffix)
	if nameWidth < 1 {
		suffix = ""
		nameWidth = max(0, width-iconWidth-1)
	}

	title := nameStyle.Render(ansi.Truncate(item.name, nameWidth, "…"))
	if suffix != "" {
		title += t.Resource.AdditionalText.Render(suffix)
	}
	return title
}
