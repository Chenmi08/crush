package completions

import (
	"cmp"
	"path/filepath"
	"slices"
	"strings"
	"sync"

	"charm.land/bubbles/v2/key"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/fsext"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/charmbracelet/x/exp/ordered"
)

const (
	minHeight = 1
	maxHeight = 10
	minWidth  = 10
	maxWidth  = 100

	tierExactName = iota
	tierPrefixName
	tierPathSegment
	tierFallback
)

// maxSkillDetailLines is the hard ceiling for a focused skill's wrapped
// description, regardless of how much screen space is available.
const maxSkillDetailLines = 8

// SelectionMsg is sent when a completion is selected.
type SelectionMsg[T any] struct {
	Value    T
	KeepOpen bool // If true, insert without closing.
}

// ClosedMsg is sent when the completions are closed.
type ClosedMsg struct{}

// CompletionItemsLoadedMsg is sent when files have been loaded for completions.
type CompletionItemsLoadedMsg struct {
	Files     []FileCompletionValue
	Resources []ResourceCompletionValue
}

// sourceMode identifies which kind of items the popup is currently
// showing. Files and skills are never mixed in one popup.
type sourceMode uint8

const (
	sourceFiles sourceMode = iota
	sourceSkills
)

// Completions represents the completions popup component.
type Completions struct {
	// Popup dimensions
	width  int
	height int

	// State
	open  bool
	query string
	mode  sourceMode

	// fixedWidth, when > 0, pins the popup width instead of sizing it
	// to the widest visible item (used by the skill popup).
	fixedWidth int
	// availableRows is the number of screen rows above the input line
	// available to the popup (0 when unknown). It bounds how far the
	// focused skill's detail block may grow.
	availableRows int
	// detailExtra is the number of wrapped description lines rendered
	// for the focused skill item.
	detailExtra int
	// focusedItem is the item currently marked focused in the list.
	// Focus is applied eagerly so the detail height is known before
	// the next render.
	focusedItem *CompletionItem

	// Key bindings
	keyMap KeyMap

	// List component
	list *list.FilterableList

	// Styling
	normalStyle      lipgloss.Style
	focusedStyle     lipgloss.Style
	matchStyle       lipgloss.Style
	descriptionStyle lipgloss.Style

	allItems []list.FilterableItem
	filtered []list.FilterableItem
}

type namePriorityRule struct {
	tier  int
	match func(pathLower, baseLower, stemLower, queryLower string) bool
}

var namePriorityRules = []namePriorityRule{
	{
		tier: tierExactName,
		match: func(_ string, baseLower, stemLower, queryLower string) bool {
			return baseLower == queryLower || stemLower == queryLower
		},
	},
	{
		tier: tierPrefixName,
		match: func(_ string, baseLower, _ string, queryLower string) bool {
			return strings.HasPrefix(baseLower, queryLower)
		},
	},
	{
		tier: tierPathSegment,
		match: func(pathLower, _ string, _ string, queryLower string) bool {
			return hasPathSegment(pathLower, queryLower)
		},
	},
}

// New creates a new completions component.
func New(normalStyle, focusedStyle, matchStyle lipgloss.Style) *Completions {
	l := list.NewFilterableList()
	l.SetGap(0)
	l.SetReverse(true)

	return &Completions{
		keyMap:       DefaultKeyMap(),
		list:         l,
		normalStyle:  normalStyle,
		focusedStyle: focusedStyle,
		matchStyle:   matchStyle,
	}
}

// SetStyles updates the styles used when rendering completion items.
// Existing items are not restyled; subsequent SetItems calls pick up the
// new styles.
func (c *Completions) SetStyles(normalStyle, focusedStyle, matchStyle lipgloss.Style) {
	c.normalStyle = normalStyle
	c.focusedStyle = focusedStyle
	c.matchStyle = matchStyle
}

// SetDescriptionStyle sets the dimmed style used for item descriptions
// and their wrapped detail lines.
func (c *Completions) SetDescriptionStyle(descriptionStyle lipgloss.Style) {
	c.descriptionStyle = descriptionStyle
}

// SetAvailableRows tells the popup how many screen rows sit above the
// input line, so the focused skill's detail block can use them without
// pushing the popup over the editor. Zero means unknown, which falls
// back to the default detail budget.
func (c *Completions) SetAvailableRows(rows int) {
	c.availableRows = rows
}

// IsOpen returns whether the completions popup is open.
func (c *Completions) IsOpen() bool {
	return c.open
}

// Query returns the current filter query.
func (c *Completions) Query() string {
	return c.query
}

// Size returns the visible size of the popup.
func (c *Completions) Size() (width, height int) {
	visible := len(c.filtered) + c.detailExtra
	return c.width, min(visible, c.height)
}

// KeyMap returns the key bindings.
func (c *Completions) KeyMap() KeyMap {
	return c.keyMap
}

// Open opens the completions with file items from the filesystem.
func (c *Completions) Open(depth, limit int) tea.Cmd {
	return func() tea.Msg {
		var msg CompletionItemsLoadedMsg
		var wg sync.WaitGroup
		wg.Go(func() {
			msg.Files = loadFiles(depth, limit)
		})
		wg.Go(func() {
			msg.Resources = loadMCPResources()
		})
		wg.Wait()
		return msg
	}
}

// SetItems sets the files and MCP resources and rebuilds the merged list.
func (c *Completions) SetItems(files []FileCompletionValue, resources []ResourceCompletionValue) {
	items := make([]list.FilterableItem, 0, len(files)+len(resources))

	// Add files first.
	for _, file := range files {
		item := NewCompletionItem(
			file.Path,
			file,
			c.normalStyle,
			c.focusedStyle,
			c.matchStyle,
		)
		items = append(items, item)
	}

	// Add MCP resources.
	for _, resource := range resources {
		item := NewCompletionItem(
			resource.MCPName+"/"+cmp.Or(resource.Title, resource.URI),
			resource,
			c.normalStyle,
			c.focusedStyle,
			c.matchStyle,
		)
		items = append(items, item)
	}

	c.fixedWidth = 0
	c.applyItems(sourceFiles, items)
}

// SetSkillItems shows user-invocable skills in the popup, pinned to
// the given width. The caller decides whether there is anything to
// show before calling.
func (c *Completions) SetSkillItems(skills []SkillCompletionValue, width int) {
	// Base rows are the list items themselves; everything left above
	// the input (capped) is fair game for the focused description.
	detailLines := defaultMaxDetailLines
	if c.availableRows > 0 {
		base := ordered.Clamp(len(skills), int(minHeight), int(maxHeight))
		detailLines = ordered.Clamp(c.availableRows-base, 0, maxSkillDetailLines)
	}

	items := make([]list.FilterableItem, 0, len(skills))
	for _, skill := range skills {
		item := NewCompletionItem(
			skill.Name,
			skill,
			c.normalStyle,
			c.focusedStyle,
			c.matchStyle,
		)
		item.descriptionStyle = c.descriptionStyle
		item.maxDetailLines = detailLines
		items = append(items, item.WithDescription(skill.Description))
	}

	c.fixedWidth = max(width, 0)
	c.applyItems(sourceSkills, items)
}

// applyItems replaces the popup contents and opens it. File items keep
// the historical bottom-to-top (reverse) order, anchored at the input;
// skills render top-to-bottom so the list reads like any other menu and
// the focused item's detail lines land below its name.
func (c *Completions) applyItems(mode sourceMode, items []list.FilterableItem) {
	c.list.SetReverse(mode == sourceFiles)
	c.open = true
	c.mode = mode
	c.query = ""
	c.allItems = items
	c.filtered = append([]list.FilterableItem(nil), items...)
	c.list.SetItems(c.filtered...)
	c.list.SetFilter("")
	c.list.Focus()
	c.list.SelectFirst()

	c.updateSize()
}

// Close closes the completions popup.
func (c *Completions) Close() {
	c.open = false
}

// Filter filters the completions with the given query.
func (c *Completions) Filter(query string) {
	if !c.open {
		return
	}

	if query == c.query {
		return
	}

	c.query = query
	c.applyNamePriorityFilter(query)

	c.list.SelectFirst()
	c.updateSize()
}

func (c *Completions) applyNamePriorityFilter(query string) {
	if query == "" {
		c.filtered = append([]list.FilterableItem(nil), c.allItems...)
		c.list.SetItems(c.filtered...)
		return
	}

	c.list.SetItems(c.allItems...)
	c.list.SetFilter(query)
	raw := c.list.FilteredItems()
	filtered := make([]list.FilterableItem, 0, len(raw))
	for _, item := range raw {
		filterable, ok := item.(list.FilterableItem)
		if !ok {
			continue
		}
		filtered = append(filtered, filterable)
	}

	queryLower := strings.ToLower(strings.TrimSpace(query))
	slices.SortStableFunc(filtered, func(a, b list.FilterableItem) int {
		return namePriorityTier(a.Filter(), queryLower) - namePriorityTier(b.Filter(), queryLower)
	})
	c.filtered = filtered
	c.list.SetItems(c.filtered...)
}

func namePriorityTier(path, queryLower string) int {
	if queryLower == "" {
		return tierFallback
	}

	pathLower := strings.ToLower(path)
	baseLower := strings.ToLower(filepath.Base(strings.ReplaceAll(path, `\`, `/`)))
	stemLower := strings.TrimSuffix(baseLower, filepath.Ext(baseLower))
	for _, rule := range namePriorityRules {
		if rule.match(pathLower, baseLower, stemLower, queryLower) {
			return rule.tier
		}
	}
	return tierFallback
}

func hasPathSegment(pathLower, queryLower string) bool {
	return slices.Contains(strings.FieldsFunc(pathLower, func(r rune) bool {
		return r == '/' || r == '\\'
	}), queryLower)
}

func (c *Completions) updateSize() {
	if c.fixedWidth > 0 {
		c.width = c.fixedWidth
	} else {
		// Measure the items that fit in a full-height viewport. Using
		// the filtered slice directly keeps the width stable
		// regardless of the previous popup's viewport size.
		width := 0
		limit := min(len(c.filtered), int(maxHeight))
		for _, item := range c.filtered[:limit] {
			s := item.(interface{ Text() string }).Text()
			width = max(width, ansi.StringWidth(s))
		}
		c.width = ordered.Clamp(width+2, int(minWidth), int(maxWidth))
	}
	c.updateHeight()
}

// updateHeight recomputes the viewport height, leaving the popup width
// untouched. It grows the viewport to fit the focused skill's wrapped
// description lines.
func (c *Completions) updateHeight() {
	c.syncFocus()
	c.detailExtra = c.selectedDetailHeight()
	c.height = ordered.Clamp(len(c.filtered), int(minHeight), int(maxHeight)) + c.detailExtra
	c.list.SetSize(c.width, c.height)
	c.list.ScrollToSelected()
}

// syncFocus eagerly mirrors the list selection onto the items so the
// focused item's detail height is available before rendering. The
// list's own render callback reapplies the same focus, which is a
// no-op.
func (c *Completions) syncFocus() {
	selected := c.list.Selected()
	var next *CompletionItem
	if selected >= 0 && selected < len(c.filtered) {
		next, _ = c.filtered[selected].(*CompletionItem)
	}
	if c.focusedItem == next {
		return
	}
	if c.focusedItem != nil {
		c.focusedItem.SetFocused(false)
	}
	if next != nil {
		next.SetFocused(true)
	}
	c.focusedItem = next
}

// selectedDetailHeight returns the number of wrapped description lines
// the focused skill adds below its row.
func (c *Completions) selectedDetailHeight() int {
	if c.mode != sourceSkills {
		return 0
	}
	selected := c.list.Selected()
	if selected < 0 || selected >= len(c.filtered) {
		return 0
	}
	item, ok := c.filtered[selected].(*CompletionItem)
	if !ok {
		return 0
	}
	return item.DetailHeight(c.width)
}

// HasItems returns whether there are visible items.
func (c *Completions) HasItems() bool {
	return len(c.filtered) > 0
}

// Update handles key events for the completions.
func (c *Completions) Update(msg tea.KeyPressMsg) (tea.Msg, bool) {
	if !c.open {
		return nil, false
	}

	switch {
	case key.Matches(msg, c.keyMap.Up):
		c.selectPrev()
		return nil, true

	case key.Matches(msg, c.keyMap.Down):
		c.selectNext()
		return nil, true

	case key.Matches(msg, c.keyMap.UpInsert):
		c.selectPrev()
		return c.selectCurrent(true), true

	case key.Matches(msg, c.keyMap.DownInsert):
		c.selectNext()
		return c.selectCurrent(true), true

	case key.Matches(msg, c.keyMap.Select):
		if !c.HasItems() {
			// Nothing to select: let the key fall through to the
			// editor so Enter still sends the message.
			return nil, false
		}
		return c.selectCurrent(false), true

	case key.Matches(msg, c.keyMap.Cancel):
		c.Close()
		return ClosedMsg{}, true
	}

	return nil, false
}

// selectPrev selects the previous item with circular navigation.
func (c *Completions) selectPrev() {
	items := c.filtered
	if len(items) == 0 {
		return
	}
	if !c.list.SelectPrev() {
		c.list.WrapToEnd()
	}
	c.updateHeight()
}

// selectNext selects the next item with circular navigation.
func (c *Completions) selectNext() {
	items := c.filtered
	if len(items) == 0 {
		return
	}
	if !c.list.SelectNext() {
		c.list.WrapToStart()
	}
	c.updateHeight()
}

// selectCurrent returns a command with the currently selected item.
func (c *Completions) selectCurrent(keepOpen bool) tea.Msg {
	items := c.filtered
	if len(items) == 0 {
		return nil
	}

	selected := c.list.Selected()
	if selected < 0 || selected >= len(items) {
		return nil
	}

	item, ok := items[selected].(*CompletionItem)
	if !ok {
		return nil
	}

	if !keepOpen {
		c.open = false
	}

	switch item := item.Value().(type) {
	case ResourceCompletionValue:
		return SelectionMsg[ResourceCompletionValue]{
			Value:    item,
			KeepOpen: keepOpen,
		}
	case FileCompletionValue:
		return SelectionMsg[FileCompletionValue]{
			Value:    item,
			KeepOpen: keepOpen,
		}
	case SkillCompletionValue:
		return SelectionMsg[SkillCompletionValue]{
			Value:    item,
			KeepOpen: keepOpen,
		}
	default:
		return nil
	}
}

// Render renders the completions popup.
func (c *Completions) Render() string {
	if !c.open {
		return ""
	}

	items := c.filtered
	if len(items) == 0 {
		return ""
	}

	return c.list.List.Render()
}

func loadFiles(depth, limit int) []FileCompletionValue {
	files, _, _ := fsext.ListDirectory(".", nil, depth, limit)
	slices.Sort(files)
	result := make([]FileCompletionValue, 0, len(files))
	for _, file := range files {
		result = append(result, FileCompletionValue{
			Path: strings.TrimPrefix(file, "./"),
		})
	}
	return result
}

func loadMCPResources() []ResourceCompletionValue {
	var resources []ResourceCompletionValue
	for mcpName, mcpResources := range mcp.Resources() {
		for _, r := range mcpResources {
			resources = append(resources, ResourceCompletionValue{
				MCPName:  mcpName,
				URI:      r.URI,
				Title:    r.Name,
				MIMEType: r.MIMEType,
			})
		}
	}
	return resources
}
