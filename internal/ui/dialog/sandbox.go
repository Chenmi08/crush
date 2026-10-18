package dialog

import (
	"charm.land/bubbles/v2/help"
	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/crush/internal/sandbox"
	"github.com/charmbracelet/crush/internal/ui/common"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/crush/internal/ui/styles"
	uv "github.com/charmbracelet/ultraviolet"
	"github.com/sahilm/fuzzy"
)

const (
	// SandboxID is the identifier for the sandbox mode picker dialog.
	SandboxID              = "sandbox_mode"
	sandboxDialogMaxWidth  = 50
	sandboxDialogMinHeight = 8
	sandboxDialogMaxHeight = 16
)

// SandboxOption represents a sandbox mode option.
type SandboxOption struct {
	Mode        sandbox.Mode
	Title       string
	Description string
}

// AllSandboxOptions lists all available sandbox modes in order, widest last.
var AllSandboxOptions = []SandboxOption{
	{Mode: sandbox.ModeReadOnly, Title: "Read Only", Description: "No writes"},
	{Mode: sandbox.ModeWorkspaceWrite, Title: "Workspace Write", Description: "Default"},
	{Mode: sandbox.ModeDangerFullAccess, Title: "Danger Full Access", Description: "No confinement"},
}

// Sandbox represents a dialog for selecting the sandbox mode.
type Sandbox struct {
	com   *common.Common
	help  help.Model
	list  *list.FilterableList
	input textinput.Model

	keyMap struct {
		Select   key.Binding
		Next     key.Binding
		Previous key.Binding
		UpDown   key.Binding
		Close    key.Binding
	}
}

// SandboxItem represents a sandbox mode list item.
type SandboxItem struct {
	*list.Versioned
	option    SandboxOption
	isCurrent bool
	t         *styles.Styles
	m         fuzzy.Match
	cache     map[int]string
	focused   bool
}

// Finished implements list.Item. Sandbox items are render-stable outside of
// explicit SetFocused / SetMatch.
func (s *SandboxItem) Finished() bool {
	return true
}

var (
	_ Dialog   = (*Sandbox)(nil)
	_ ListItem = (*SandboxItem)(nil)
)

// NewSandbox creates a new sandbox mode picker dialog.
func NewSandbox(com *common.Common) *Sandbox {
	s := &Sandbox{com: com}

	h := help.New()
	h.Styles = com.Styles.DialogHelpStyles()
	s.help = h

	s.list = list.NewFilterableList()
	s.list.Focus()

	s.input = textinput.New()
	s.input.SetVirtualCursor(false)
	s.input.Placeholder = "Type to filter"
	s.input.SetStyles(com.Styles.TextInput)
	s.input.Focus()

	s.keyMap.Select = key.NewBinding(
		key.WithKeys("enter", "ctrl+y"),
		key.WithHelp("enter", "confirm"),
	)
	s.keyMap.Next = key.NewBinding(
		key.WithKeys("down", "ctrl+n"),
		key.WithHelp("↓", "next item"),
	)
	s.keyMap.Previous = key.NewBinding(
		key.WithKeys("up", "ctrl+p"),
		key.WithHelp("↑", "previous item"),
	)
	s.keyMap.UpDown = key.NewBinding(
		key.WithKeys("up", "down"),
		key.WithHelp("↑/↓", "choose"),
	)
	s.keyMap.Close = CloseKey

	s.setItems()
	return s
}

// ID implements Dialog.
func (s *Sandbox) ID() string {
	return SandboxID
}

// Reset refreshes the list so it highlights the sandbox mode currently in
// effect.
func (s *Sandbox) Reset() {
	s.setItems()
}

// HandleMsg implements [Dialog].
func (s *Sandbox) HandleMsg(msg tea.Msg) Action {
	switch msg := msg.(type) {
	case tea.KeyPressMsg:
		switch {
		case key.Matches(msg, s.keyMap.Close):
			return ActionClose{}
		case key.Matches(msg, s.keyMap.Previous):
			s.list.Focus()
			if s.list.IsSelectedFirst() {
				s.list.SelectLast()
				s.list.ScrollToBottom()
				break
			}
			s.list.SelectPrev()
			s.list.ScrollToSelected()
		case key.Matches(msg, s.keyMap.Next):
			s.list.Focus()
			if s.list.IsSelectedLast() {
				s.list.SelectFirst()
				s.list.ScrollToTop()
				break
			}
			s.list.SelectNext()
			s.list.ScrollToSelected()
		case key.Matches(msg, s.keyMap.Select):
			selectedItem := s.list.SelectedItem()
			if selectedItem == nil {
				break
			}
			sandboxItem, ok := selectedItem.(*SandboxItem)
			if !ok {
				break
			}
			return ActionSetSandboxMode{Mode: string(sandboxItem.option.Mode)}
		default:
			prevValue := s.input.Value()
			var cmd tea.Cmd
			s.input, cmd = s.input.Update(msg)
			value := s.input.Value()
			if value != prevValue {
				s.list.SetFilter(value)
				s.list.ScrollToTop()
				s.list.SetSelected(0)
			}

			return ActionCmd{cmd}
		}
	}
	return nil
}

// Cursor returns the cursor position relative to the dialog.
func (s *Sandbox) Cursor() *tea.Cursor {
	return InputCursor(s.com.Styles, s.input.Cursor())
}

// Draw implements [Dialog].
func (s *Sandbox) Draw(scr uv.Screen, area uv.Rectangle) *tea.Cursor {
	t := s.com.Styles
	width := max(0, min(sandboxDialogMaxWidth, area.Dx()-t.Dialog.View.GetHorizontalBorderSize()))
	innerWidth := width - t.Dialog.View.GetHorizontalFrameSize()

	s.input.SetWidth(dialogInputTextWidth(t, s.input, innerWidth))

	// Size the dialog to fit the list content, clamped to min/max bounds.
	listTotalHeight := s.list.TotalHeight()
	heightOffset := t.Dialog.Title.GetVerticalFrameSize() + titleContentHeight +
		t.Dialog.InputPrompt.GetVerticalFrameSize() + inputContentHeight +
		t.Dialog.HelpView.GetVerticalFrameSize() +
		t.Dialog.View.GetVerticalFrameSize()
	desiredHeight := heightOffset + listTotalHeight
	maxAvailable := area.Dy() - t.Dialog.View.GetVerticalBorderSize()
	height := max(sandboxDialogMinHeight, min(sandboxDialogMaxHeight, desiredHeight, maxAvailable))

	listHeight, listTotalHeight, _ := sizeDialogList(t, s.list, innerWidth, height)

	rc := NewRenderContext(t, width)
	rc.Title = "Select Sandbox Mode"
	inputView := t.Dialog.InputPrompt.Render(s.input.View())
	rc.AddPart(inputView)

	visibleCount := len(s.list.FilteredItems())
	if s.list.Height() >= visibleCount {
		s.list.ScrollToTop()
	} else {
		s.list.ScrollToSelected()
	}

	listView := t.Dialog.List.Height(s.list.Height()).Render(s.list.Render())
	listView = joinScrollbar(t, listView, listHeight, listTotalHeight, listHeight, s.list.Offset())
	rc.AddPart(listView)
	rc.Help = renderDialogHelp(t, &s.help, s, innerWidth)

	view := rc.Render()

	cur := s.Cursor()
	DrawCenterCursor(scr, area, view, cur)
	return cur
}

// ShortHelp implements [help.KeyMap].
func (s *Sandbox) ShortHelp() []key.Binding {
	return []key.Binding{
		s.keyMap.UpDown,
		s.keyMap.Select,
		s.keyMap.Close,
	}
}

// FullHelp implements [help.KeyMap].
func (s *Sandbox) FullHelp() [][]key.Binding {
	m := [][]key.Binding{}
	slice := []key.Binding{
		s.keyMap.Select,
		s.keyMap.Next,
		s.keyMap.Previous,
		s.keyMap.Close,
	}
	for i := 0; i < len(slice); i += 4 {
		end := min(i+4, len(slice))
		m = append(m, slice[i:end])
	}
	return m
}

func (s *Sandbox) setItems() {
	currentMode := s.com.Workspace.SandboxMode()

	items := make([]list.FilterableItem, 0, len(AllSandboxOptions))
	selectedIndex := 0
	for _, option := range AllSandboxOptions {
		item := &SandboxItem{
			Versioned: list.NewVersioned(),
			option:    option,
			isCurrent: string(option.Mode) == currentMode,
			t:         s.com.Styles,
		}
		if string(option.Mode) == currentMode {
			selectedIndex = len(items)
		}
		items = append(items, item)
	}

	s.list.SetItems(items...)
	s.list.SetSelected(selectedIndex)
	s.list.ScrollToSelected()
}

// Filter returns the filter value for the sandbox mode item.
func (s *SandboxItem) Filter() string {
	return s.option.Title
}

// ID returns the unique identifier for the sandbox mode.
func (s *SandboxItem) ID() string {
	return string(s.option.Mode)
}

// SetFocused sets the focus state of the sandbox mode item.
func (s *SandboxItem) SetFocused(focused bool) {
	if s.focused == focused {
		return
	}
	s.cache = nil
	s.focused = focused
	if s.Versioned != nil {
		s.Bump()
	}
}

// SetMatch sets the fuzzy match for the sandbox mode item.
func (s *SandboxItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(s.m, m) {
		return
	}
	s.cache = nil
	s.m = m
	if s.Versioned != nil {
		s.Bump()
	}
}

// Render returns the string representation of the sandbox mode item.
func (s *SandboxItem) Render(width int) string {
	info := s.option.Description
	if s.isCurrent {
		info = "current"
	}
	st := ListItemStyles{
		ItemBlurred:     s.t.Dialog.NormalItem,
		ItemFocused:     s.t.Dialog.SelectedItem,
		InfoTextBlurred: s.t.Dialog.ListItem.InfoBlurred,
		InfoTextFocused: s.t.Dialog.ListItem.InfoFocused,
	}
	return renderItem(st, s.option.Title, info, s.focused, width, s.cache, &s.m)
}
