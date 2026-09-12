package completions

import (
	"strings"
	"testing"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

func TestFitDescriptionPrefersFirstSentence(t *testing.T) {
	t.Parallel()

	desc := "Review the changes since a fixed point along two axes. Then report them."
	got, truncated := fitDescription(desc, 60)
	require.True(t, truncated)
	require.Equal(t, "Review the changes since a fixed point along two axes.", got)

	// Falls back to a grapheme hard truncation when even the first
	// sentence does not fit.
	got, truncated = fitDescription(desc, 20)
	require.True(t, truncated)
	require.LessOrEqual(t, ansi.StringWidth(got), 20)
	require.True(t, strings.HasSuffix(got, "…"))
}

func TestNormalizeDescription(t *testing.T) {
	t.Parallel()
	require.Equal(t, "a b c", normalizeDescription("  a\n\tb   c "))
}

func TestSkillItemFiltersByNameOnly(t *testing.T) {
	t.Parallel()

	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetSkillItems([]SkillCompletionValue{
		{ID: "grilling", Name: "grilling", Description: "design tree interview"},
	}, 60)

	c.Filter("tree")
	require.False(t, c.HasItems())

	c.Filter("gril")
	require.True(t, c.HasItems())
}

func TestSkillItemSelectsValue(t *testing.T) {
	t.Parallel()

	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetSkillItems([]SkillCompletionValue{
		{ID: "/skills/grilling/SKILL.md", Name: "grilling", Description: "d"},
	}, 60)

	msg, handled := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.True(t, handled)
	sel, ok := msg.(SelectionMsg[SkillCompletionValue])
	require.True(t, ok)
	require.Equal(t, "grilling", sel.Value.Name)
	require.Equal(t, "/skills/grilling/SKILL.md", sel.Value.ID)
}

func TestSelectWithoutItemsFallsThrough(t *testing.T) {
	t.Parallel()

	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetSkillItems([]SkillCompletionValue{{ID: "grilling", Name: "grilling"}}, 60)
	c.Filter("zzz")
	require.False(t, c.HasItems())

	msg, handled := c.Update(tea.KeyPressMsg{Code: tea.KeyEnter})
	require.False(t, handled)
	require.Nil(t, msg)
}

func TestSetSkillItemsPinsWidth(t *testing.T) {
	t.Parallel()

	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetSkillItems([]SkillCompletionValue{{Name: "grilling"}}, 40)

	width, _ := c.Size()
	require.Equal(t, 40, width)
}

func TestSkillItemDetailHeightAndRender(t *testing.T) {
	t.Parallel()

	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetSkillItems([]SkillCompletionValue{
		{Name: "short", Description: "a short description"},
		{Name: "long", Description: strings.Repeat("word ", 40)},
	}, 60)

	// The first item is selected: short description, no detail rows.
	_, height := c.Size()
	require.Equal(t, 2, height)

	// Move to the long description: the viewport grows by its two
	// wrapped detail lines.
	c.selectNext()
	_, height = c.Size()
	require.Equal(t, 4, height)

	item, ok := c.filtered[1].(*CompletionItem)
	require.True(t, ok)
	require.Equal(t, 2, item.DetailHeight(60))

	rendered := c.Render()
	require.Contains(t, rendered, "long")
	require.Greater(t, strings.Count(rendered, "\n"), 1)
}

func TestSkillItemDetailHiddenForShortDescription(t *testing.T) {
	t.Parallel()

	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetSkillItems([]SkillCompletionValue{
		{Name: "grilling", Description: "short"},
	}, 60)

	item, ok := c.filtered[0].(*CompletionItem)
	require.True(t, ok)
	require.Equal(t, 0, item.DetailHeight(60))

	_, height := c.Size()
	require.Equal(t, 1, height)
}

func TestSkillCompletionKeyMapSelects(t *testing.T) {
	t.Parallel()

	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetSkillItems([]SkillCompletionValue{{ID: "grilling", Name: "grilling"}}, 60)
	require.True(t, key.Matches(tea.KeyPressMsg{Code: tea.KeyEnter}, c.KeyMap().Select))
}

func TestSkillItemDetailReadsTopToBottom(t *testing.T) {
	t.Parallel()

	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetSkillItems([]SkillCompletionValue{{
		Name:        "long",
		Description: "Alpha beta gamma delta epsilon zeta. Eta theta iota kappa lambda mu.",
	}}, 30)

	rendered := c.Render()
	require.Contains(t, rendered, "Alpha")
	require.Contains(t, rendered, "Eta")
	require.Less(t, strings.Index(rendered, "Alpha"), strings.Index(rendered, "Eta"))
	// The detail lines must render below the item's name.
	require.Less(t, strings.Index(rendered, "long"), strings.Index(rendered, "Eta"))
}

func TestSkillItemDetailGrowsWithAvailableRows(t *testing.T) {
	t.Parallel()

	long := strings.Repeat("word ", 200)
	skills := []SkillCompletionValue{
		{Name: "alpha", Description: long},
		{Name: "beta", Description: long},
		{Name: "gamma", Description: long},
	}

	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())

	// Three list rows leave two rows for the description.
	c.SetAvailableRows(5)
	c.SetSkillItems(skills, 60)
	item := c.filtered[0].(*CompletionItem)
	require.Equal(t, 2, item.DetailHeight(60))

	// One free row: the description is capped at one line.
	c.SetAvailableRows(4)
	c.SetSkillItems(skills, 60)
	item = c.filtered[0].(*CompletionItem)
	require.Equal(t, 1, item.DetailHeight(60))

	// No free rows: no detail block at all.
	c.SetAvailableRows(3)
	c.SetSkillItems(skills, 60)
	item = c.filtered[0].(*CompletionItem)
	require.Equal(t, 0, item.DetailHeight(60))

	// A tall screen still caps at maxSkillDetailLines.
	c.SetAvailableRows(100)
	c.SetSkillItems(skills, 60)
	item = c.filtered[0].(*CompletionItem)
	require.Equal(t, maxSkillDetailLines, item.DetailHeight(60))
}

func TestSkillItemRenderedLinesFitWidth(t *testing.T) {
	t.Parallel()

	c := New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	c.SetSkillItems([]SkillCompletionValue{{
		Name:        "grilling",
		Description: strings.Repeat("very long description ", 20),
	}}, 30)

	for _, line := range strings.Split(c.Render(), "\n") {
		require.LessOrEqual(t, ansi.StringWidth(line), 30, "line %q overflows the popup width", line)
	}
}
