package common

import (
	"cmp"
	"fmt"
	"image/color"
	"strconv"
	"strings"
	"time"

	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/crush/internal/agent/hyper"
	"github.com/charmbracelet/crush/internal/home"
	"github.com/charmbracelet/crush/internal/session"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"golang.org/x/text/cases"
	"golang.org/x/text/language"
)

// PrettyPath formats a file path with home directory shortening and applies
// muted styling.
func PrettyPath(t *styles.Styles, path string, width int) string {
	formatted := home.Short(path)
	return t.Sidebar.WorkingDir.Width(width).Render(formatted)
}

// FormatReasoningEffort formats a reasoning effort level for display.
func FormatReasoningEffort(effort string) string {
	if effort == "xhigh" {
		return "X-High"
	}
	return cases.Title(language.English).String(effort)
}

// ModelContextInfo contains token usage and cost information for a model.
type ModelContextInfo struct {
	ContextUsed    int64
	ModelContext   int64
	Cost           float64
	EstimatedUsage bool
	// Stats holds runtime statistics for the most recent model step.
	Stats session.StepStats
}

// ModelInfo renders model information including name, provider, reasoning
// settings, and optional context usage/cost.
func ModelInfo(t *styles.Styles, modelName, providerName, reasoningInfo string, context *ModelContextInfo, width int, hyperCredits *int) string {
	modelIcon := t.ModelInfo.Icon.Render(styles.ModelIcon)
	modelName = t.ModelInfo.Name.Render(modelName)

	// Build first line with model name and optionally provider on the same line
	var firstLine string
	if providerName != "" {
		providerInfo := t.ModelInfo.Provider.Render(fmt.Sprintf("via %s", providerName))
		modelWithProvider := fmt.Sprintf("%s %s %s", modelIcon, modelName, providerInfo)

		// Check if it fits on one line
		if lipgloss.Width(modelWithProvider) <= width {
			firstLine = modelWithProvider
		} else {
			// If it doesn't fit, put provider on next line
			firstLine = fmt.Sprintf("%s %s", modelIcon, modelName)
		}
	} else {
		firstLine = fmt.Sprintf("%s %s", modelIcon, modelName)
	}

	parts := []string{firstLine}

	// If provider didn't fit on first line, add it as second line
	if providerName != "" && !strings.Contains(firstLine, "via") {
		providerInfo := fmt.Sprintf("via %s", providerName)
		parts = append(parts, t.ModelInfo.ProviderFallback.Render(providerInfo))
	}

	if reasoningInfo != "" {
		parts = append(parts, t.ModelInfo.Reasoning.Render(reasoningInfo))
	}

	if context != nil {
		formattedInfo := formatTokensAndCost(t, context.ContextUsed, context.ModelContext, context.Cost, context.EstimatedUsage)
		parts = append(parts, lipgloss.NewStyle().PaddingLeft(2).Render(formattedInfo))
		if stats := formatStepStats(t, context, width); stats != "" {
			parts = append(parts, lipgloss.NewStyle().PaddingLeft(2).Render(stats))
		}
	}

	if providerName == hyper.DisplayName && hyperCredits != nil {
		hcInfo := t.ModelInfo.HypercreditIcon.Render(styles.HypercreditIcon)
		hcInfo += " "
		hcInfo += t.ModelInfo.HypercreditText.Render(fmt.Sprintf("%s Hypercredits", FormatCredits(*hyperCredits)))
		parts = append(parts, "", hcInfo)
	}

	return lipgloss.NewStyle().Width(width).Render(
		lipgloss.JoinVertical(lipgloss.Left, parts...),
	)
}

// formatTokensAndCost formats token usage and cost with appropriate units
// (K/M) and percentage of context window.
func formatTokensAndCost(t *styles.Styles, tokens, contextWindow int64, cost float64, estimated bool) string {
	formattedTokens := formatTokenCount(tokens)

	var percentage float64
	if contextWindow > 0 {
		percentage = (float64(tokens) / float64(contextWindow)) * 100
	}

	formattedCost := t.ModelInfo.Cost.Render(fmt.Sprintf("$%.2f", cost))

	formattedTokens = t.ModelInfo.TokenCount.Render(fmt.Sprintf("(%s)", formattedTokens))
	percentageText := fmt.Sprintf("%d%%", int(percentage))
	if estimated {
		percentageText = "~" + percentageText
	}
	formattedPercentage := t.ModelInfo.TokenPercentage.Render(percentageText)
	formattedTokens = fmt.Sprintf("%s %s", formattedPercentage, formattedTokens)
	if percentage > 80 {
		formattedTokens = fmt.Sprintf("%s %s", styles.LSPWarningIcon, formattedTokens)
	}

	return fmt.Sprintf("%s %s", formattedTokens, formattedCost)
}

// formatStepStats renders runtime statistics for the most recent model
// step as two semantic rows: response timing (time to first token and
// generation speed) and token accounting (the step's input tokens with
// the cache hit rate, plus the generated output tokens), e.g.
// "0.8s · 42.3 tok/s" and "↑15.2K 92.00% · ↓1.2K". The token row always
// wraps below the timing row; a row that does not fit on a single line
// splits into one stat per line. It returns an empty string when there
// are no stats to show.
func formatStepStats(t *styles.Styles, context *ModelContextInfo, width int) string {
	stats := context.Stats

	var timing, tokens []string
	if stats.TTFT > 0 {
		timing = append(timing, formatStepDuration(stats.TTFT))
	}
	if stats.TokensPerSecond > 0 {
		timing = append(timing, formatTokensPerSecond(stats.TokensPerSecond, context.EstimatedUsage))
	}
	if cache := formatCacheStats(stats); cache != "" {
		tokens = append(tokens, cache)
	}
	if output := formatOutputStats(stats, context.EstimatedUsage); output != "" {
		tokens = append(tokens, output)
	}

	if len(timing) == 0 && len(tokens) == 0 {
		return ""
	}

	// ModelInfo pads the stats block by two columns.
	available := width - 2

	// Timing and token accounting never share a line; a row that does
	// not fit splits into one stat per line.
	var lines []string
	for _, group := range [][]string{timing, tokens} {
		if len(group) == 0 {
			continue
		}
		if line := joinStatLines(group...); lipgloss.Width(line) <= available {
			lines = append(lines, line)
			continue
		}
		lines = append(lines, group...)
	}
	return renderStatLines(t, lines...)
}

// formatTokensPerSecond formats the generation speed, prefixing estimated
// values with a tilde, e.g. "42.3 tok/s" or "~10.0 tok/s".
func formatTokensPerSecond(tps float64, estimated bool) string {
	tpsFormat := "%.1f tok/s"
	if tps >= 100 {
		tpsFormat = "%.0f tok/s"
	}
	formatted := fmt.Sprintf(tpsFormat, tps)
	if estimated {
		formatted = "~" + formatted
	}
	return formatted
}

// renderStatLines styles each stat line individually and stacks them.
// Rendering line by line avoids the block padding lipgloss applies to
// multi-line strings.
func renderStatLines(t *styles.Styles, lines ...string) string {
	rendered := make([]string, 0, len(lines))
	for _, line := range lines {
		if line != "" {
			rendered = append(rendered, t.ModelInfo.Stats.Render(line))
		}
	}
	return strings.Join(rendered, "\n")
}

// joinStatLines joins non-empty stats with a middle dot.
func joinStatLines(parts ...string) string {
	var nonEmpty []string
	for _, part := range parts {
		if part != "" {
			nonEmpty = append(nonEmpty, part)
		}
	}
	return strings.Join(nonEmpty, " · ")
}

// formatCacheStats renders the step's input tokens with an upward arrow
// and the cache hit rate, e.g. "↑15.2K 99.00%". The hit rate is omitted
// when unknown. It returns an empty string when no tokens were served
// from the cache or the input total is unknown.
func formatCacheStats(stats session.StepStats) string {
	if stats.CacheReadTokens == 0 || stats.TotalPromptTokens == 0 {
		return ""
	}
	cache := fmt.Sprintf("↑%s", formatTokenCount(stats.TotalPromptTokens))
	if stats.CacheHitRate > 0 {
		cache += fmt.Sprintf(" %.2f%%", stats.CacheHitRate*100)
	}
	return cache
}

// formatOutputStats renders the generated output token count with a
// downward arrow, e.g. "↓1.2K", pairing it with the upward arrow used
// for the cache reads beside it.
func formatOutputStats(stats session.StepStats, estimated bool) string {
	if stats.OutputTokens == 0 {
		return ""
	}
	tokens := formatTokenCount(stats.OutputTokens)
	if estimated {
		tokens = "~" + tokens
	}
	return "↓" + tokens
}

// formatStepDuration formats a model step duration compactly for the
// sidebar, e.g. "812ms", "0.8s", or "12s".
func formatStepDuration(d time.Duration) string {
	switch {
	case d < 100*time.Millisecond:
		return fmt.Sprintf("%dms", d.Milliseconds())
	case d < 10*time.Second:
		return fmt.Sprintf("%.1fs", d.Seconds())
	default:
		return fmt.Sprintf("%.0fs", d.Seconds())
	}
}

// formatTokenCount formats a token count with K/M units.
func formatTokenCount(tokens int64) string {
	var formatted string
	switch {
	case tokens >= 1_000_000:
		formatted = fmt.Sprintf("%.1fM", float64(tokens)/1_000_000)
	case tokens >= 1_000:
		formatted = fmt.Sprintf("%.1fK", float64(tokens)/1_000)
	default:
		formatted = fmt.Sprintf("%d", tokens)
	}

	if strings.HasSuffix(formatted, ".0K") {
		formatted = strings.Replace(formatted, ".0K", "K", 1)
	}
	if strings.HasSuffix(formatted, ".0M") {
		formatted = strings.Replace(formatted, ".0M", "M", 1)
	}
	return formatted
}

// FormatCredits formats an integer with comma separators for thousands.
func FormatCredits(n int) string {
	s := strconv.FormatInt(int64(n), 10)
	if n < 1000 {
		return s
	}
	// Calculate how many digits before the first comma.
	firstGroup := len(s) % 3
	if firstGroup == 0 {
		firstGroup = 3
	}
	var b []byte
	for i := 0; i < len(s); i++ {
		if i > 0 && i == firstGroup {
			b = append(b, ',')
			firstGroup += 3
		}
		b = append(b, s[i])
	}
	return string(b)
}

// StatusOpts defines options for rendering a status line with icon, title,
// description, and optional extra content.
type StatusOpts struct {
	Icon             string // if empty no icon will be shown
	Title            string
	TitleColor       color.Color
	Description      string
	DescriptionColor color.Color
	ExtraContent     string // additional content to append after the description
}

// Status renders a status line with icon, title, description, and extra
// content. The description is truncated if it exceeds the available width.
func Status(t *styles.Styles, opts StatusOpts, width int) string {
	icon := opts.Icon
	title := opts.Title
	description := opts.Description

	titleColor := cmp.Or(opts.TitleColor, t.Resource.DefaultTitleFg)
	descriptionColor := cmp.Or(opts.DescriptionColor, t.Resource.DefaultDescFg)

	title = t.Resource.RowTitleBase.Foreground(titleColor).Render(title)

	if description != "" {
		extraContentWidth := lipgloss.Width(opts.ExtraContent)
		if extraContentWidth > 0 {
			extraContentWidth += 1
		}
		description = ansi.Truncate(description, width-lipgloss.Width(icon)-lipgloss.Width(title)-2-extraContentWidth, "…")
		description = t.Resource.RowDescBase.Foreground(descriptionColor).Render(description)
	}

	var content []string
	if icon != "" {
		content = append(content, icon)
	}
	content = append(content, title)
	if description != "" {
		content = append(content, description)
	}
	if opts.ExtraContent != "" {
		content = append(content, opts.ExtraContent)
	}

	return strings.Join(content, " ")
}

// Section renders a section header with a title and a horizontal line filling
// the remaining width.
func Section(t *styles.Styles, text string, width int, info ...string) string {
	char := styles.SectionSeparator
	length := lipgloss.Width(text) + 1
	remainingWidth := width - length

	var infoText string
	if len(info) > 0 {
		infoText = strings.Join(info, " ")
		if len(infoText) > 0 {
			infoText = " " + infoText
			remainingWidth -= lipgloss.Width(infoText)
		}
	}

	text = t.Section.Title.Render(text)
	if remainingWidth > 0 {
		text = text + " " + t.Section.Line.Render(strings.Repeat(char, remainingWidth)) + infoText
	}
	return text
}

// DialogTitle renders a dialog title with a decorative line filling the
// remaining width. When the title alone exceeds the available width it is
// truncated with an ellipsis so it never wraps.
func DialogTitle(t *styles.Styles, title string, width int, fromColor, toColor color.Color) string {
	if width > 0 && lipgloss.Width(title) > width {
		return ansi.Truncate(title, width, "…")
	}
	char := "╱"
	length := lipgloss.Width(title) + 1
	remainingWidth := width - length
	if remainingWidth > 0 {
		lines := strings.Repeat(char, remainingWidth)
		lines = styles.ApplyForegroundGrad(t.Dialog.TitleLineBase, lines, fromColor, toColor)
		title = title + " " + lines
	}
	return title
}
