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
	t := m.com.Styles

	title := t.Resource.Heading.Render("Skills")
	if isSection {
		title = common.Section(t, title, width)
	}

	items := m.skillStatusItems()
	if len(items) == 0 {
		list := t.Resource.AdditionalText.Render("None")
		return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
	}

	list := skillsList(t, items, width, maxItems)
	return lipgloss.NewStyle().Width(width).Render(fmt.Sprintf("%s\n\n%s", title, list))
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

func (m *UI) skillStatusItems() []skillStatusItem {
	t := m.com.Styles
	var items []skillStatusItem
	stateNames := make(map[string]struct{}, len(m.skillStates))

	disabledSet := make(map[string]bool)
	if m.com != nil && m.com.Workspace != nil {
		if cfg := m.com.Config(); cfg != nil {
			for _, name := range cfg.Options.DisabledSkills {
				disabledSet[name] = true
			}
		}
	}

	states := slices.Clone(m.skillStates)
	slices.SortStableFunc(states, func(a, b *skills.SkillState) int {
		return strings.Compare(a.Path, b.Path)
	})
	for _, state := range states {
		name := state.Name
		if name == "" {
			name = filepath.Base(filepath.Dir(state.Path))
		}
		if disabledSet[name] {
			continue
		}
		if _, exists := stateNames[name]; exists {
			continue
		}
		stateNames[name] = struct{}{}
		icon := t.Resource.OnlineIcon.String()
		suffix := ""
		if state.State == skills.StateError {
			icon = t.Resource.ErrorIcon.String()
		} else {
			suffix = skillInvocationSuffix(state.UserInvocable, state.DisableModelInvocation)
		}
		items = append(items, skillStatusItem{
			icon:   icon,
			name:   name,
			suffix: suffix,
		})
	}

	builtin := cachedBuiltinSkills()
	slices.SortStableFunc(builtin, func(a, b *skills.Skill) int {
		return strings.Compare(a.Name, b.Name)
	})
	for _, skill := range builtin {
		if _, ok := stateNames[skill.Name]; ok {
			continue
		}
		if disabledSet[skill.Name] {
			continue
		}
		items = append(items, skillStatusItem{
			icon:   t.Resource.OnlineIcon.String(),
			name:   skill.Name,
			suffix: skillInvocationSuffix(skill.UserInvocable, skill.DisableModelInvocation),
		})
	}

	slices.SortStableFunc(items, func(a, b skillStatusItem) int {
		return strings.Compare(a.name, b.name)
	})

	return items
}

func skillsList(t *styles.Styles, items []skillStatusItem, width, maxItems int) string {
	if maxItems <= 0 {
		return ""
	}

	if len(items) > maxItems {
		visibleItems := items[:maxItems-1]
		remaining := len(items) - (maxItems - 1)
		items = append(visibleItems, skillStatusItem{
			name:    "more",
			display: t.Resource.AdditionalText.Render(fmt.Sprintf("…and %d more", remaining)),
		})
	}

	renderedItems := make([]string, 0, len(items))
	for _, item := range items {
		renderedItems = append(renderedItems, common.Status(t, common.StatusOpts{
			Icon:        item.icon,
			Title:       item.title(t, width),
			Description: item.description,
		}, width))
	}
	return lipgloss.JoinVertical(lipgloss.Left, renderedItems...)
}

// title renders a row title, eliding the skill name so the row
// (icon + name + suffix) fits in width instead of wrapping. The invocation
// suffix stays visible; only the name is truncated. If the column is too
// narrow for even a minimal suffix, the suffix is dropped.
func (item skillStatusItem) title(t *styles.Styles, width int) string {
	if item.display != "" {
		return item.display
	}

	iconWidth := lipgloss.Width(item.icon)
	suffix := item.suffix

	// One cell is reserved for the space between the icon and the title.
	nameWidth := width - iconWidth - 1 - lipgloss.Width(suffix)
	if nameWidth < 1 {
		suffix = ""
		nameWidth = max(0, width-iconWidth-1)
	}

	title := t.Resource.Name.Render(ansi.Truncate(item.name, nameWidth, "…"))
	if suffix != "" {
		title += t.Resource.AdditionalText.Render(suffix)
	}
	return title
}
