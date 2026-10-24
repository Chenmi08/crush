package agent

import (
	"regexp"
	"strings"
)

// analysisBlockHead matches the opening of a scratchpad block the summary
// prompt permits the model to write before the summary itself. It is greedy
// only up to the first closing tag (the `.*?` is non-greedy), so a second
// block later in the reply is left alone.
var analysisBlockHead = regexp.MustCompile(`(?is)^\s*<analysis>.*?</analysis>`)

// stripAnalysisBlock removes a leading `<analysis>...</analysis>` scratchpad
// from a completed summary. The prompt allows the block so the model can
// think before writing, but it must never reach the summary message: the
// summary becomes the session's only context, and the scratchpad is both
// noise and, unlike the rest of the document, never reviewed for accuracy.
//
// Only a block at the very start is removed. A mention of `<analysis>` in the
// middle of the prose is part of the summary and is kept.
func stripAnalysisBlock(s string) string {
	stripped := analysisBlockHead.ReplaceAllString(s, "")
	if stripped == s {
		return s
	}
	return strings.TrimSpace(stripped)
}
