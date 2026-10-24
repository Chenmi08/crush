package agent

import (
	"errors"

	"charm.land/fantasy"
)

// errSummaryTruncated reports a summarization stream the provider stopped at
// the output limit. The summary content in that case is a prefix of what the
// model meant to write, so it can never become the session's only context.
var errSummaryTruncated = errors.New("summary truncated at output limit")

// summaryOutcome inspects the terminal state of a completed summarization
// stream. A provider that stops on [fantasy.FinishReasonLength] produced a
// partial summary, which is a hard rejection rather than a warning: the
// caller must leave the previous summary anchor in place and persist the
// summary message as failed instead of successful.
//
// Output size is deliberately not returned: size guards measure the
// persisted content with [summaryCompletionTokens], so a count taken here
// could only disagree with the stored value.
//
// A nil result, an unknown finish reason, and an empty finish reason all
// pass. Some providers do not report one at all, and treating "unknown" as
// truncation would discard usable summaries.
func summaryOutcome(resp *fantasy.AgentResult) error {
	if resp == nil {
		return nil
	}
	if resp.Response.FinishReason == fantasy.FinishReasonLength {
		return errSummaryTruncated
	}
	return nil
}
