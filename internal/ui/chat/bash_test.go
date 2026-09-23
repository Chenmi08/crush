package chat

import (
	"fmt"
	"strings"
	"testing"

	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// TestBashToolMessageItem_CommandAlwaysExpanded guards the contract
// that the bash command is always rendered expanded (newlines
// preserved) regardless of the expansion state, while the output body
// remains expandable and defaults to collapsed.
func TestBashToolMessageItem_CommandAlwaysExpanded(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{
		ID:       "bash1",
		Name:     "bash",
		Input:    `{"command":"echo one\necho two\necho three"}`,
		Finished: true,
	}

	render := func(expanded bool) string {
		ctx := &BashToolRenderContext{}
		return ctx.RenderTool(&sty, 120, &ToolRenderOpts{
			ToolCall:        tc,
			Result:          &message.ToolResult{ToolCallID: "bash1", Content: "out"},
			Status:          ToolStatusSuccess,
			ExpandedContent: expanded,
		})
	}

	for _, expanded := range []bool{false, true} {
		out := ansi.Strip(render(expanded))
		require.Contains(t, out, "echo one")
		require.Contains(t, out, "echo two")
		require.Contains(t, out, "echo three")
	}
}

// TestBashToolMessageItem_LongCommandNotTruncated verifies that a
// long bash command wraps in the header instead of being truncated
// with an ellipsis, even when the output is collapsed.
func TestBashToolMessageItem_LongCommandNotTruncated(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	longCmd := "echo " + strings.Repeat("a", 200)
	tc := message.ToolCall{
		ID:       "bash3",
		Name:     "bash",
		Input:    `{"command":"` + longCmd + `"}`,
		Finished: true,
	}
	ctx := &BashToolRenderContext{}
	out := ansi.Strip(ctx.RenderTool(&sty, 120, &ToolRenderOpts{
		ToolCall:        tc,
		Result:          &message.ToolResult{ToolCallID: "bash3", Content: "out"},
		Status:          ToolStatusSuccess,
		ExpandedContent: false,
	}))

	require.NotContains(t, out, "…", "collapsed bash command must not be truncated")
	require.Contains(t, out, longCmd[strings.LastIndex(longCmd, "a")-10:], "end of command must be visible")
}

// TestBashToolMessageItem_OutputDefaultsCollapsed verifies that bash
// output is truncated when collapsed and fully shown when expanded.
func TestBashToolMessageItem_OutputDefaultsCollapsed(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{
		ID:       "bash2",
		Name:     "bash",
		Input:    `{"command":"seq 20"}`,
		Finished: true,
	}
	var lines []string
	for i := 1; i <= 20; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	result := &message.ToolResult{ToolCallID: "bash2", Content: strings.Join(lines, "\n")}

	item := NewBashToolMessageItem(&sty, tc, result, false, "")
	bash, ok := item.(*BashToolMessageItem)
	require.True(t, ok, "NewBashToolMessageItem must return a *BashToolMessageItem")
	require.False(t, bash.expandedContent, "bash items must default to collapsed")

	renderCollapsed := ansi.Strip(bash.RawRender(120))
	require.Contains(t, renderCollapsed, "line10")
	require.NotContains(t, renderCollapsed, "line20")
	require.Contains(t, renderCollapsed, "… (10 lines hidden)")

	require.True(t, bash.ToggleExpanded(), "first toggle should report expanded")
	require.Contains(t, ansi.Strip(bash.RawRender(120)), "line20")
}

// TestBashToolMessageItem_ShowsRewrittenCommand verifies that when the
// bash tool reports an executed command (e.g. after a PreToolUse hook
// rewrote the input), the rendered header shows that command instead of
// the tool call's original input.
func TestBashToolMessageItem_ShowsRewrittenCommand(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{
		ID:       "bash-rewrite",
		Name:     "bash",
		Input:    `{"command":"echo original"}`,
		Finished: true,
	}
	ctx := &BashToolRenderContext{}
	out := ansi.Strip(ctx.RenderTool(&sty, 120, &ToolRenderOpts{
		ToolCall: tc,
		Result: &message.ToolResult{
			ToolCallID: "bash-rewrite",
			Content:    "rewritten output",
			Metadata:   `{"command":"echo rewritten"}`,
		},
		Status:          ToolStatusSuccess,
		ExpandedContent: false,
	}))

	require.Contains(t, out, "echo rewritten", "reported executed command must be displayed")
	require.NotContains(t, out, "echo original", "original command must not be displayed")
}

// TestBashToolMessageItem_NoRewriteKeepsOriginal verifies that when the
// bash tool reports the same command it was given, the original is shown
// unchanged.
func TestBashToolMessageItem_NoRewriteKeepsOriginal(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{
		ID:       "bash-norewrite",
		Name:     "bash",
		Input:    `{"command":"echo plain"}`,
		Finished: true,
	}
	ctx := &BashToolRenderContext{}
	out := ansi.Strip(ctx.RenderTool(&sty, 120, &ToolRenderOpts{
		ToolCall: tc,
		Result: &message.ToolResult{
			ToolCallID: "bash-norewrite",
			Content:    "plain output",
			Metadata:   `{"command":"echo plain"}`,
		},
		Status:          ToolStatusSuccess,
		ExpandedContent: false,
	}))

	require.Contains(t, out, "echo plain", "original command must be displayed when nothing was rewritten")
}

// TestBashToolMessageItem_BackgroundShowsRewrittenCommand verifies that a
// background job whose command was rewritten shows the reported executed
// command, matching the foreground path.
func TestBashToolMessageItem_BackgroundShowsRewrittenCommand(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	tc := message.ToolCall{
		ID:       "bash-bg",
		Name:     "bash",
		Input:    `{"command":"echo original"}`,
		Finished: true,
	}
	ctx := &BashToolRenderContext{}
	out := ansi.Strip(ctx.RenderTool(&sty, 120, &ToolRenderOpts{
		ToolCall: tc,
		Result: &message.ToolResult{
			ToolCallID: "bash-bg",
			Content:    "background started",
			Metadata:   `{"command":"echo rewritten","description":"echo rewritten","background":true,"shell_id":"job1"}`,
		},
		Status:          ToolStatusSuccess,
		ExpandedContent: false,
	}))

	require.Contains(t, out, "echo rewritten", "background job must show the reported executed command")
	require.NotContains(t, out, "echo original", "background job must not show the original command")
}

// TestToolOutputPlainContent_SingleHiddenLineShown verifies that
// collapsing never hides just one line: content one line over the
// collapsed limit is shown in full, while more over that truncates.
func TestToolOutputPlainContent_SingleHiddenLineShown(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	var lines []string
	for i := 1; i <= 11; i++ {
		lines = append(lines, fmt.Sprintf("line%d", i))
	}
	out := ansi.Strip(toolOutputPlainContent(&sty, strings.Join(lines, "\n"), 80, false))
	require.Contains(t, out, "line11")
	require.NotContains(t, out, "hidden")

	lines = append(lines, "line12")
	out = ansi.Strip(toolOutputPlainContent(&sty, strings.Join(lines, "\n"), 80, false))
	require.NotContains(t, out, "line12")
	require.Contains(t, out, "2 lines hidden")
}
