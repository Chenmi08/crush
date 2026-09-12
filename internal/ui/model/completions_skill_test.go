package model

import (
	"testing"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/skills"
	"github.com/charmbracelet/crush/internal/ui/attachments"
	"github.com/charmbracelet/crush/internal/ui/completions"
	"github.com/charmbracelet/crush/internal/ui/dialog"
	"github.com/stretchr/testify/require"
)

var skillDollarKey = tea.KeyPressMsg{Code: '$', Text: "$"}

func newSkillTestUI() *UI {
	u := newTestUI()
	u.dialog = dialog.NewOverlay()
	u.attachments = attachments.New(
		attachments.NewRenderer(
			lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle(),
			lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle(),
		),
		attachments.Keymap{},
	)
	u.completions = completions.New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	u.skillCatalog = []skills.CatalogEntry{
		{ID: "grilling", Name: "grilling", Description: "design tree interview", UserInvocable: true},
	}
	u.skillCatalogLoaded = true
	return u
}

func TestSkillTriggerOpensPopup(t *testing.T) {
	t.Parallel()

	u := newSkillTestUI()
	u.handleKeyPressMsg(skillDollarKey)

	require.True(t, u.completionsOpen)
	require.Equal(t, completionSkills, u.completionsKind)
	require.Equal(t, "$", u.textarea.Value())
	require.True(t, u.completions.HasItems())
}

func TestSkillTriggerRequiresWhitespaceBoundary(t *testing.T) {
	t.Parallel()

	u := newSkillTestUI()
	u.textarea.SetValue("cost")
	u.textarea.MoveToEnd()
	u.handleKeyPressMsg(skillDollarKey)

	require.False(t, u.completionsOpen)
	require.Equal(t, "cost$", u.textarea.Value())
}

func TestSkillTriggerWorksAfterWhitespace(t *testing.T) {
	t.Parallel()

	u := newSkillTestUI()
	u.textarea.SetValue("cost ")
	u.textarea.MoveToEnd()
	u.handleKeyPressMsg(skillDollarKey)

	require.True(t, u.completionsOpen)
	require.Equal(t, completionSkills, u.completionsKind)
}

func TestSkillTriggerDisabledInBangMode(t *testing.T) {
	t.Parallel()

	u := newSkillTestUI()
	u.bangMode = true
	u.handleKeyPressMsg(skillDollarKey)

	require.False(t, u.completionsOpen)
	require.Equal(t, "$", u.textarea.Value())
}

func TestTextareaCursorOffset(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	u.textarea.SetWidth(80)

	require.Equal(t, 0, u.textareaCursorOffset())

	u.textarea.SetValue("hello")
	require.Equal(t, len("hello"), u.textareaCursorOffset())

	u.textarea.MoveToBegin()
	u.textarea.SetCursorColumn(2)
	require.Equal(t, 2, u.textareaCursorOffset())

	// Multibyte runes count in bytes, not runes.
	u.textarea.SetValue("héllo")
	u.textarea.MoveToBegin()
	u.textarea.SetCursorColumn(2)
	require.Equal(t, len("hé"), u.textareaCursorOffset())

	// Move to the second logical line.
	u.textarea.SetValue("abc\ndef")
	u.textarea.MoveToBegin()
	u.textarea.CursorDown()
	u.textarea.SetCursorColumn(2)
	require.Equal(t, len("abc\n")+2, u.textareaCursorOffset())
}

func TestUserInvocableSkillsFiltersAndSorts(t *testing.T) {
	t.Parallel()

	u := &UI{
		skillCatalog: []skills.CatalogEntry{
			{ID: "model-only", Name: "model-only"},
			{ID: "beta", Name: "beta", Description: "b", Source: skills.SourceProject, UserInvocable: true},
			{ID: "alpha", Name: "Alpha", Description: "a", Source: skills.SourceUser, UserInvocable: true},
		},
	}

	got := u.userInvocableSkills()
	require.Len(t, got, 2)
	require.Equal(t, "Alpha", got[0].Name)
	require.Equal(t, "alpha", got[0].ID)
	require.Equal(t, "a", got[0].Description)
	require.Equal(t, "beta", got[1].Name)
}

func TestSkillCompletionsWidth(t *testing.T) {
	t.Parallel()

	u := &UI{isCompact: true, width: 100}
	require.Equal(t, 64, u.skillCompletionsWidth())

	u.width = 20
	require.Equal(t, 24, u.skillCompletionsWidth())
}

func TestReplaceCompletionText(t *testing.T) {
	t.Parallel()

	u := newTestUI()

	// Inserting at the end of the input appends a trailing space.
	u.textarea.SetValue("$gri")
	u.textarea.MoveToEnd()
	u.completionsStartIndex = 0
	require.True(t, u.replaceCompletionText("$grilling"))
	require.Equal(t, "$grilling ", u.textarea.Value())

	// Mid-line replacement leaves the text after the cursor intact and
	// does not append a space.
	u.textarea.SetValue("ask $gri now")
	u.textarea.MoveToBegin()
	u.textarea.SetCursorColumn(8)
	u.completionsStartIndex = 4
	require.True(t, u.replaceCompletionText("$grilling"))
	require.Equal(t, "ask $grilling now", u.textarea.Value())
	require.Equal(t, len("ask $grilling"), u.textareaCursorOffset())
}

func TestHasSkillAttachment(t *testing.T) {
	t.Parallel()

	atts := attachments.New(
		attachments.NewRenderer(
			lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle(),
			lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle(),
		),
		attachments.Keymap{},
	)
	require.True(t, atts.Update(message.Attachment{FilePath: "grilling"}))

	u := &UI{attachments: atts}
	require.True(t, u.hasSkillAttachment("grilling"))
	require.False(t, u.hasSkillAttachment("code-review"))
}

func TestFilterSkillCompletions(t *testing.T) {
	t.Parallel()

	u := newTestUI()
	comp := completions.New(lipgloss.NewStyle(), lipgloss.NewStyle(), lipgloss.NewStyle())
	comp.SetSkillItems([]completions.SkillCompletionValue{
		{ID: "grilling", Name: "grilling"},
		{ID: "code-review", Name: "code-review"},
	}, 40)
	u.completions = comp
	u.completionsOpen = true
	u.completionsKind = completionSkills
	u.completionsStartIndex = 2

	u.textarea.SetValue("> $gri")
	u.textarea.MoveToEnd()
	u.filterSkillCompletions()
	require.True(t, u.completionsOpen)
	require.Equal(t, "gri", u.completionsQuery)
	require.True(t, u.completions.HasItems())

	// A space ends the reference and closes the popup.
	u.textarea.SetValue("> $gri ")
	u.textarea.MoveToEnd()
	u.filterSkillCompletions()
	require.False(t, u.completionsOpen)

	// No matches also closes the popup so Enter can send.
	comp.SetSkillItems([]completions.SkillCompletionValue{
		{ID: "grilling", Name: "grilling"},
	}, 40)
	u.completionsOpen = true
	u.textarea.SetValue("> $zzz")
	u.textarea.MoveToEnd()
	u.filterSkillCompletions()
	require.False(t, u.completionsOpen)
}
