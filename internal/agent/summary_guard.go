package agent

import (
	"errors"
	"strings"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/session"
)

// Errors returned by the summary guards. Each one is a hard rejection: the
// caller keeps the previous summary anchor in place, so the session stays
// usable with the context it already had.
var (
	// errSummaryTruncated reports a summarization stream the provider
	// stopped at the output limit. The summary content in that case is a
	// prefix of what the model meant to write, so it can never become the
	// session's only context.
	errSummaryTruncated = errors.New("summary truncated at output limit")

	// errSummaryCollapsed reports a summary that lost most of the volume of
	// the summary it replaces. This is the summary-of-summary failure mode:
	// the model condenses the previous summary instead of merging it with
	// the new turns, and the result is a shorter document that claims to
	// describe a longer history.
	errSummaryCollapsed = errors.New("summary was less than half the size of the summary it replaces")

	// errSummaryInflated reports a summary larger than the context it
	// replaces, which would grow the conversation instead of compacting it.
	errSummaryInflated = errors.New("summary was larger than the context it replaces")

	// errSummaryDisabled reports that automatic summarization was skipped
	// because the session already failed too many times in a row. It is
	// not a turn failure: the caller continues without compacting.
	errSummaryDisabled = errors.New("automatic summarization is paused for this session")
)

// maxSummaryFailures is the number of consecutive guarded summarization
// failures after which a session stops trying to summarize automatically.
// Without it, a session the model cannot summarize is retried on every
// turn forever, and each retry is a full-context request.
const maxSummaryFailures = 3

// summaryCollapseRatio is the fraction of the previous summary's size a new
// summary must reach to be accepted. A document that drops below half has
// lost information, not compressed it.
const summaryCollapseRatio = 0.5

// summaryCollapseRetries is the number of extra attempts a collapsed summary
// gets. One is deliberate: a summarizer that condensed the previous summary
// away once may do it again, and a second rejection should keep the old
// context rather than burn more turns.
const summaryCollapseRetries = 1

// summaryCollapseRetrySuffix is appended to the prompt for the single retry
// a collapsed summary gets. It names the failure rather than repeating the
// template's general instructions, which the model already followed once.
const summaryCollapseRetrySuffix = "\n\n## Retry: the previous attempt collapsed\n\n" +
	"Your previous summary was less than half the size of the summary it " +
	"replaces. Merge the earlier summary with the conversation above instead " +
	"of condensing it: keep every fact, decision, rejected approach and file " +
	"reference that is still relevant, and do not shorten the user's original " +
	"messages. A long document is the expected outcome here."

// summarySizes are the token counts [validateSummary] compares. A struct is
// used rather than three bare int64s because two of the fields are document
// sizes and one is a context size, and swapping them is a silent bug.
type summarySizes struct {
	// PrevTokens is the size of the narrative written by the summary being
	// replaced. It is 0 when the session has no previous summary, which
	// makes the collapse check inert.
	PrevTokens int64

	// NewTokens is the size of the narrative just written. Both sides of
	// the ratio guard are measured the same way, so provider usage
	// accounting cannot make a healthy summary look like a collapse.
	NewTokens int64

	// PrevContext is the size of the context being replaced, i.e. the
	// prompt and completion counters before they are reset. It is 0 only
	// before the session has taken a step, which makes the inflation check
	// inert.
	PrevContext int64
}

// validateSummary applies the size guards to a completed summary. It is pure
// so the thresholds can be tested without a provider, a session or a clock.
//
// Collapse is checked first: a document can be both smaller than its
// predecessor and larger than the context it replaces only in pathological
// counters, and reporting the information loss is the more useful of the two.
func validateSummary(s summarySizes) error {
	if s.PrevTokens > 0 && float64(s.NewTokens) < float64(s.PrevTokens)*summaryCollapseRatio {
		return errSummaryCollapsed
	}
	if s.PrevContext > 0 && s.NewTokens >= s.PrevContext {
		return errSummaryInflated
	}
	return nil
}

// summaryOutcome inspects the terminal state of a completed summarization
// stream. A provider that stops on [fantasy.FinishReasonLength] produced a
// partial summary, which is a hard rejection rather than a warning: the
// caller must leave the previous summary anchor in place and persist the
// summary message as failed instead of successful.
//
// Output size is deliberately not returned: the size guards measure the
// persisted content, so a count taken here could only disagree with it.
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

// summarizeOutcome classifies the error an automatic compaction returns from
// the point of view of the turn that triggered it. A tripped breaker means
// "continue the turn without compacting" (skip), while a guard rejection or a
// provider failure means "the turn cannot continue safely" (fatal).
//
// Skipping is deliberately not a turn failure (D11): a summarization problem
// must not become a conversation problem. The caller still takes the normal
// resume path when tool calls are pending, so a turn that was cut short for
// compaction is completed by the queued continuation — which then runs
// uncompacted because [sessionAgent.Run]'s StopWhen consults the same breaker.
func summarizeOutcome(err error) (skip bool, fatal error) {
	if err == nil {
		return false, nil
	}
	if errors.Is(err, errSummaryDisabled) {
		return true, nil
	}
	return false, err
}

// summaryBreakerTripped reports whether a session has failed summarization
// enough times in a row that it should stop trying. It is pure so the
// policy can be tested without a session service.
func summaryBreakerTripped(s session.Session) bool {
	return s.SummaryFailCount >= maxSummaryFailures
}

// summaryNarrativeTokens measures the model-written part of a summary. The
// appendix — the manifest, the transcript pointer, whatever else code appends
// — is added after the prose; counting it would inflate a size comparison and
// make the collapse guard more permissive than it should be.
//
// The boundary comes from [narrativeCut] rather than a single marker: a
// summary written before the appendix existed may carry only one of the old
// blocks, and cutting on the manifest alone would count that block as prose.
//
// Both sides of the ratio are measured with this helper on purpose. Provider
// usage counts are not comparable to a byte-derived estimate, and after the
// blocks are appended they also describe a different string, so mixing the
// two would let the guard and the stored counters drift apart.
func summaryNarrativeTokens(content string) int64 {
	return approxTokenCount(strings.TrimSpace(content[:narrativeCut(content)]))
}
