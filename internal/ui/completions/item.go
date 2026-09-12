package completions

import (
	"slices"
	"strings"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/ui/list"
	"github.com/charmbracelet/x/ansi"
	"github.com/rivo/uniseg"
	"github.com/sahilm/fuzzy"
)

const (
	// descriptionGap separates the item name from its inline
	// description.
	descriptionGap = 2
	// minDescriptionWidth is the narrowest inline description budget
	// worth rendering. Below it the row shows only the name.
	minDescriptionWidth = 8
	// detailIndent indents wrapped description lines under the name.
	detailIndent = "  "
	// defaultMaxDetailLines caps the focused item's wrapped description
	// when the caller doesn't provide an available-space budget.
	defaultMaxDetailLines = 2
)

// FileCompletionValue represents a file path completion value.
type FileCompletionValue struct {
	Path string
}

// ResourceCompletionValue represents a MCP resource completion value.
type ResourceCompletionValue struct {
	MCPName  string
	URI      string
	Title    string
	MIMEType string
}

// SkillCompletionValue represents a user-invocable skill completion
// value.
type SkillCompletionValue struct {
	ID          string
	Name        string
	Description string
}

// CompletionItem represents an item in the completions list.
type CompletionItem struct {
	*list.Versioned

	text    string
	value   any
	match   fuzzy.Match
	focused bool
	cache   map[int]string

	// description is the normalized, dimmed text rendered after the
	// name. Empty for non-skill items. When it doesn't fit inline, the
	// focused item additionally renders wrapped detail lines.
	description string
	// maxDetailLines caps those wrapped detail lines. 0 disables the
	// detail block entirely.
	maxDetailLines int

	// Styles
	normalStyle      lipgloss.Style
	focusedStyle     lipgloss.Style
	matchStyle       lipgloss.Style
	descriptionStyle lipgloss.Style
}

// NewCompletionItem creates a new completion item.
func NewCompletionItem(text string, value any, normalStyle, focusedStyle, matchStyle lipgloss.Style) *CompletionItem {
	return &CompletionItem{
		Versioned:      list.NewVersioned(),
		text:           text,
		value:          value,
		normalStyle:    normalStyle,
		focusedStyle:   focusedStyle,
		matchStyle:     matchStyle,
		maxDetailLines: defaultMaxDetailLines,
	}
}

// WithDescription attaches a dimmed description rendered after the item
// name. It returns the item for chaining.
func (c *CompletionItem) WithDescription(description string) *CompletionItem {
	c.description = normalizeDescription(description)
	c.cache = nil
	return c
}

// Finished implements list.Item. Completion items render purely from
// (text, match, focus); any mutation (SetMatch / SetFocused) bumps
// Version() so the frozen cache entry invalidates on the next
// render. Marking them finished lets the F6 list memo skip the
// per-line work for the steady completions popup.
func (c *CompletionItem) Finished() bool {
	return true
}

// Text returns the display text of the item.
func (c *CompletionItem) Text() string {
	return c.text
}

// Value returns the value of the item.
func (c *CompletionItem) Value() any {
	return c.value
}

// Filter implements [list.FilterableItem]. Only the name is matched;
// the description is display-only.
func (c *CompletionItem) Filter() string {
	return c.text
}

// SetMatch implements [list.MatchSettable].
func (c *CompletionItem) SetMatch(m fuzzy.Match) {
	if sameFuzzyMatch(c.match, m) {
		return
	}
	c.cache = nil
	c.match = m
	c.Bump()
}

// sameFuzzyMatch reports whether two fuzzy.Match values are
// observably equal. Because Match contains a slice (MatchedIndexes)
// it is not directly comparable with ==; we compare the scalar
// fields and then walk the indexes. SetMatch uses this to skip
// gratuitous version bumps when the same match is reapplied.
func sameFuzzyMatch(a, b fuzzy.Match) bool {
	return a.Str == b.Str &&
		a.Index == b.Index &&
		a.Score == b.Score &&
		slices.Equal(a.MatchedIndexes, b.MatchedIndexes)
}

// SetFocused implements [list.Focusable].
func (c *CompletionItem) SetFocused(focused bool) {
	if c.focused == focused {
		return
	}
	c.cache = nil
	c.focused = focused
	c.Bump()
}

// DetailHeight returns the number of extra lines the item renders when
// focused (0 or up to maxDetailLines). It is used to grow the popup's
// viewport so the wrapped description is not clipped.
func (c *CompletionItem) DetailHeight(width int) int {
	if !c.focused || c.description == "" || width < 2 {
		return 0
	}
	_, _, _, detail := c.row(width - 2)
	return len(detail)
}

// Render implements list.Item.
func (c *CompletionItem) Render(width int) string {
	if c.cache == nil {
		c.cache = make(map[int]string)
	}
	if cached, ok := c.cache[width]; ok {
		return cached
	}

	style := c.normalStyle
	if c.focused {
		style = c.focusedStyle
	}

	innerWidth := width - 2 // Account for padding.
	if innerWidth < 1 {
		c.cache[width] = style.Padding(0, 1).Width(width).Render("")
		return c.cache[width]
	}

	row, descStart, descEnd, detail := c.row(innerWidth)
	content := style.Padding(0, 1).Width(width).Render(row)
	content = c.applyRanges(content, row, descStart, descEnd, style)

	if c.focused && len(detail) > 0 {
		detailStyle := c.descriptionStyle.Background(style.GetBackground())
		lines := make([]string, 1, len(detail)+1)
		lines[0] = content
		for _, line := range detail {
			line = ansi.Truncate(detailIndent+line, innerWidth, "…")
			lines = append(lines, detailStyle.Padding(0, 1).Width(width).Render(line))
		}
		content = strings.Join(lines, "\n")
	}

	c.cache[width] = content
	return content
}

// row lays out the item's single-line row plus, when the description
// doesn't fit inline, the wrapped detail lines shown while focused.
// descStart and descEnd are byte indexes into the returned row bounding
// the description segment.
func (c *CompletionItem) row(innerWidth int) (row string, descStart, descEnd int, detail []string) {
	name := c.text
	desc := c.description
	if desc == "" {
		return ansi.Truncate(name, innerWidth, "…"), 0, 0, nil
	}

	budget := innerWidth - lipgloss.Width(name) - descriptionGap
	if budget < minDescriptionWidth {
		return ansi.Truncate(name, innerWidth, "…"), 0, 0, nil
	}

	short, truncated := fitDescription(desc, budget)
	row = name + strings.Repeat(" ", descriptionGap) + short
	descStart = len(name) + descriptionGap
	descEnd = min(descStart+len(short), len(row))
	if truncated {
		detail = wrapLines(desc, innerWidth-len(detailIndent), c.maxDetailLines)
	}
	return row, descStart, descEnd, detail
}

// applyRanges overlays the fuzzy match highlight on the name and the
// description style on the description segment of a rendered row.
// Ranges are expressed in visible cells, offset by the row padding.
func (c *CompletionItem) applyRanges(content, row string, descStart, descEnd int, style lipgloss.Style) string {
	var ranges []lipgloss.Range
	matchStyle := c.matchStyle.Background(style.GetBackground())
	for _, rng := range matchedRanges(c.match.MatchedIndexes) {
		start, stop := bytePosToVisibleCharPos(row, rng)
		ranges = append(ranges, lipgloss.NewRange(start+1, stop+2, matchStyle))
	}
	if descStart > 0 && descEnd > descStart && descEnd <= len(row) {
		start := ansi.StringWidth(row[:descStart]) + 1
		end := start + ansi.StringWidth(row[descStart:descEnd])
		ranges = append(ranges, lipgloss.NewRange(start, end+1, c.descriptionStyle.Background(style.GetBackground())))
	}
	return lipgloss.StyleRanges(content, ranges...)
}

// normalizeDescription collapses all whitespace runs to single spaces
// so multi-line metadata stays on one row.
func normalizeDescription(description string) string {
	return strings.Join(strings.Fields(description), " ")
}

// fitDescription returns a description shortened to fit the given
// budget. It prefers the first sentence when that fits; otherwise it
// hard-truncates on a grapheme boundary with an ellipsis. The boolean
// reports whether the returned text is shorter than the input.
func fitDescription(description string, budget int) (string, bool) {
	if budget <= 0 {
		return "", description != ""
	}
	if ansi.StringWidth(description) <= budget {
		return description, false
	}
	if sentence := firstSentence(description); sentence != "" && ansi.StringWidth(sentence) <= budget {
		return sentence, true
	}
	return ansi.Truncate(description, budget, "…"), true
}

// firstSentence returns description up to and including its first
// sentence terminator, or "" when none is present.
func firstSentence(description string) string {
	best := -1
	for _, sep := range []string{". ", "! ", "? ", "; ", "。", "！", "？", "；"} {
		i := strings.Index(description, sep)
		if i < 0 {
			continue
		}
		if end := i + len(sep); best < 0 || end < best {
			best = end
		}
	}
	if best < 0 {
		return ""
	}
	return strings.TrimSpace(description[:best])
}

// wrapLines word-wraps s to the given width and returns at most limit
// lines, truncating the final line with an ellipsis when more content
// remains.
func wrapLines(s string, width, limit int) []string {
	if s == "" || width < 1 || limit < 1 {
		return nil
	}
	lines := strings.Split(ansi.Wordwrap(s, width, ""), "\n")
	if len(lines) > limit {
		lines = lines[:limit]
		lines[limit-1] = ansi.Truncate(lines[limit-1], width, "…")
	}
	return lines
}

// matchedRanges converts a list of match indexes into contiguous ranges.
func matchedRanges(in []int) [][2]int {
	if len(in) == 0 {
		return [][2]int{}
	}
	current := [2]int{in[0], in[0]}
	if len(in) == 1 {
		return [][2]int{current}
	}
	var out [][2]int
	for i := 1; i < len(in); i++ {
		if in[i] == current[1]+1 {
			current[1] = in[i]
		} else {
			out = append(out, current)
			current = [2]int{in[i], in[i]}
		}
	}
	out = append(out, current)
	return out
}

// bytePosToVisibleCharPos converts byte positions to visible character positions.
func bytePosToVisibleCharPos(str string, rng [2]int) (int, int) {
	bytePos, byteStart, byteStop := 0, rng[0], rng[1]
	pos, start, stop := 0, 0, 0
	gr := uniseg.NewGraphemes(str)
	for byteStart > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	start = pos
	for byteStop > bytePos {
		if !gr.Next() {
			break
		}
		bytePos += len(gr.Str())
		pos += max(1, gr.Width())
	}
	stop = pos
	return start, stop
}

// Ensure CompletionItem implements the required interfaces.
var (
	_ list.Item           = (*CompletionItem)(nil)
	_ list.FilterableItem = (*CompletionItem)(nil)
	_ list.MatchSettable  = (*CompletionItem)(nil)
	_ list.Focusable      = (*CompletionItem)(nil)
)
