package agent

import (
	"fmt"
	"strings"

	"github.com/charmbracelet/crush/internal/message"
)

// narrativeMarkers introduce the blocks that code appends to a summary after
// the model's prose. The manifest comes first when it has anything to report,
// and the transcript block follows it; either can be present on its own.
//
// They are a single slice so the boundary search has one definition. Anything
// that needs "where does the model's prose end" must use [narrativeCut] rather
// than looking for one marker: a summary with no failures and no changed files
// has a transcript but no manifest.
var narrativeMarkers = []string{manifestLeadIn, transcriptBlockLeadIn}

// mergeInstruction renders the instruction appended to the summary prompt when
// a previous summary exists. Re-summarizing a summary is what makes the second
// and later compactions drift: the model condenses its own earlier prose
// instead of merging the new turns into it, and each pass loses volume the
// conversation can no longer supply.
//
// The instruction names the earlier document as input rather than as a summary
// to rewrite, and forbids shrinking it. The size guards reject a collapse, but
// a rejected compaction costs a whole round-trip; the prompt is where the
// failure is cheap to avoid.
//
// The token count is a floor for the output, not a countdown of the remaining
// window (D9 forbids the latter): "do not shrink it" is only actionable next
// to a number, and the number is the document being merged, not the budget.
func mergeInstruction(previous string) string {
	return fmt.Sprintf(
		"## Merging a previous summary\n\n"+
			"The conversation above starts with a summary written earlier in this "+
			"session (about %d tokens), and the rest is everything that happened "+
			"since. Merge the two into one document about the whole session:\n\n"+
			"- Keep every fact from the earlier summary that is still true. It is "+
			"input, not a draft: do not condense it, and do not drop a detail because "+
			"the new turns did not mention it again.\n"+
			"- Add what the new turns established. When they contradict the earlier "+
			"summary, keep the newer fact and mark the older one "+
			"`superseded by: <the newer fact>`.\n"+
			"- Keep the earlier summary's decisions, rejected approaches and file "+
			"breadcrumbs, including the ones this span did not revisit.\n"+
			"- Do not write less than the earlier summary did unless facts were "+
			"genuinely resolved or superseded. A merged document that is shorter "+
			"than the one it replaces has usually lost information.",
		approxTokenCount(previous),
	)
}

// narrativeCut returns the byte offset where the model-written prose of a
// stored summary ends: the start of the first code-appended block, or the
// length of the content when there is none.
func narrativeCut(content string) int {
	cut := len(content)
	for _, marker := range narrativeMarkers {
		if i := strings.Index(content, marker); i >= 0 && i < cut {
			cut = i
		}
	}
	return cut
}

// splitPreviousSummary returns the model-written narrative of a summary.
//
// prefixLen is the byte length recorded when the summary was written, which is
// exact and cheap: it was measured before anything was appended. It is 0 for a
// summary stored before the column existed, in which case the narrative is
// recovered by cutting at the first code-appended block — the same boundary the
// size guard has always used.
func splitPreviousSummary(m message.Message, prefixLen int) string {
	content := m.Content().Text
	if prefixLen > 0 && prefixLen <= len(content) {
		return strings.TrimSpace(content[:prefixLen])
	}
	return strings.TrimSpace(content[:narrativeCut(content)])
}

// summaryAnchorID returns the id of the oldest message a summary replaces: the
// start of the span the next merge covers. msgs is the exported span, which
// Summarize only reaches when it is non-empty, so its first element is either
// the previous summary (it is being replaced) or the oldest message in the
// session.
func summaryAnchorID(msgs []message.Message) string {
	if len(msgs) == 0 {
		return ""
	}
	return msgs[0].ID
}
