package agent

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/x/ansi"
)

// elideMode is the config's elision mode under the name the agent uses. It
// aliases the config type rather than defining a second enumeration, so the
// three values and their spellings have exactly one definition.
type elideMode = config.CompactionElideMode

const (
	// elideOff leaves every payload in place.
	elideOff = config.CompactionElideOff
	// elideShadow computes the elision, logs what it would save, and sends
	// the full content. It is how the feature is measured before the
	// behavior changes, and it is the default.
	elideShadow = config.CompactionElideShadow
	// elideOn replaces eligible payloads with a tombstone.
	elideOn = config.CompactionElideOn
)

// elideModeFromOption resolves the configured value. The empty value means
// shadow.
//
// An unrecognised value cannot reach production: the shell config rejects it
// while parsing and [config.Load] rejects it for hand-edited files. Resolving
// one to shadow here means a caller that skipped both paths gets the
// documented default rather than silently eliding.
func elideModeFromOption(v config.CompactionElideMode) elideMode {
	switch v {
	case "", elideShadow:
		return elideShadow
	case elideOff:
		return elideOff
	case elideOn:
		return elideOn
	}
	return elideShadow
}

const (
	// elideKeepRecent is how many of the newest tool results always stay
	// verbatim. The model is most likely to still be reasoning about them,
	// and a tombstone in their place would force a retrieval for something
	// that was in context a moment ago.
	elideKeepRecent = 3
	// elideKeepBytes is the size below which eliding is not worth a
	// retrieval. Replacing a short result with a tombstone saves little and
	// costs the model a tool call to get it back.
	elideKeepBytes = 2048
	// tombstoneMaxBytes caps a rendered tombstone. The whole point is that
	// it is cheaper than the payload it replaces; a preview that grows past
	// this defeats it.
	tombstoneMaxBytes = 400
	// tombstonePreviewBytes caps one preview line.
	tombstonePreviewBytes = 160
)

// elidePolicy is the decision rule for which tool results leave the prompt.
// It is a plain value so [shouldElide] stays pure and the thresholds can be
// tested without an agent, a config or a clock.
type elidePolicy struct {
	Mode elideMode
	// KeepRecent is the number of trailing tool results that are never
	// elided, counted over the flattened result order.
	KeepRecent int
	// KeepBytes is the content size at or below which a result is kept.
	KeepBytes int
	// ExemptTools never elided. `view` is how the model looks at a file it
	// is editing and a diagnostics payload is the one thing it needs while
	// fixing a compile error.
	ExemptTools map[string]bool
}

// defaultElidePolicy returns the shipped policy: shadow mode, last three
// results kept, small results kept, views and diagnostics kept.
func defaultElidePolicy() elidePolicy {
	return elidePolicy{
		Mode:       elideShadow,
		KeepRecent: elideKeepRecent,
		KeepBytes:  elideKeepBytes,
		ExemptTools: map[string]bool{
			tools.ViewToolName:        true,
			tools.DiagnosticsToolName: true,
		},
	}
}

// elidePolicyFromConfig reads the user's choice of elision mode. Everything
// else in the policy is a constant: the thresholds exist to be tuned from
// measurement, and letting them be configured now would trade a constant for
// a support surface nobody has asked for.
func elidePolicyFromConfig(cfg *config.ConfigStore) elidePolicy {
	policy := defaultElidePolicy()
	if cfg == nil || cfg.Config() == nil {
		return policy
	}
	options := cfg.Config().Options
	if options == nil {
		return policy
	}
	policy.Mode = elideModeFromOption(options.CompactionElide)
	return policy
}

// shouldElide reports whether the tool result at idx in the flattened result
// order may be replaced by a tombstone. exempt holds the ids of results
// produced during the current turn, which are always kept: a result the model
// asked for with `recall` in this turn must not be elided before the turn
// ends, or the retrieval turns into a loop.
//
// Results carrying binary data are never elided. The tombstone replaces the
// text, and `recall` cannot serve media back, so eliding one would be a
// permanent loss rather than a reversible transform.
func shouldElide(results []message.ToolResult, idx int, policy elidePolicy, exempt map[string]struct{}) bool {
	if policy.Mode == elideOff {
		return false
	}
	if idx < 0 || idx >= len(results) {
		return false
	}
	r := results[idx]
	if _, keep := exempt[r.ToolCallID]; keep {
		return false
	}
	if policy.KeepRecent > 0 && idx >= len(results)-policy.KeepRecent {
		return false
	}
	if policy.ExemptTools[r.Name] {
		return false
	}
	if r.Data != "" {
		return false
	}
	return len(r.Content) > policy.KeepBytes
}

// renderTombstone renders the placeholder that takes a tool result's place in
// the prompt. It is deliberately rendered in the result's original position:
// the model reads a uniform, short marker next to the call it belongs to,
// with the handle it needs to fetch the payload back.
//
// Layout:
//
//	[elided] tool_call_id=… name=… exit=… bytes=… at=…
//	  <first non-empty line>
//	  <first line naming a failure, when different>
//	  ... (N more lines) → recall(tool_call_id="…")
//
// The header is one line and its fields are ordered by how a caller uses
// them: the handle, then what produced it, then whether it failed, then what
// a retrieval costs, then how stale it is.
func renderTombstone(r message.ToolResult, at int64) string {
	header := tombstoneHeader(r, at)
	body := ansi.Strip(r.Content)
	previews := tombstonePreviews(body)
	footer := tombstoneFooter(r.ToolCallID, tombstoneLineCount(body)-len(previews))

	var sb strings.Builder
	sb.WriteString(header)
	for _, line := range previews {
		sb.WriteString("  ")
		sb.WriteString(clipToBytes(line, tombstonePreviewBytes))
		sb.WriteString("\n")
	}
	sb.WriteString(footer)

	tombstone := sb.String()
	if len(tombstone) <= tombstoneMaxBytes {
		return tombstone
	}
	// Over budget: the previews go first. A header plus a handle is the
	// minimum useful tombstone, and it is what the cap applies to next:
	// a pathological call id or tool name must not break the promise.
	return clipToBytes(header+tombstoneFooter(r.ToolCallID, tombstoneLineCount(body)), tombstoneMaxBytes)
}

// tombstoneHeader renders the one-line header of a tombstone. The exit status
// is the reason a bash result is worth retrieving: it is read from the tool's
// own status line, and the name gate matters because a `view` whose tail
// quotes "Exit code 3" is not a failure.
func tombstoneHeader(r message.ToolResult, at int64) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "[elided] tool_call_id=%s name=%s", r.ToolCallID, r.Name)
	if tools.IsBashToolName(r.Name) {
		if code, ok := tools.ParseExitCode(r.Content); ok {
			fmt.Fprintf(&sb, " exit=%d", code)
		}
	}
	fmt.Fprintf(&sb, " bytes=%d at=%s\n", len(r.Content), time.Unix(at, 0).UTC().Format(time.RFC3339))
	return sb.String()
}

// tombstoneFooter renders the retrieval handle. It is the part that must
// never be dropped, so the line count is what gets omitted when nothing
// remains to count.
func tombstoneFooter(toolCallID string, remainingLines int) string {
	if remainingLines > 0 {
		return fmt.Sprintf("... (%d more lines) → recall(tool_call_id=%q)", remainingLines, toolCallID)
	}
	return fmt.Sprintf("... → recall(tool_call_id=%q)", toolCallID)
}

// tombstonePreviews picks at most two lines from an elided body: the first
// non-empty line, which is usually the command or the file header, and the
// first line that names a failure, which is usually why the result matters.
// The second is dropped when it is the first.
func tombstonePreviews(body string) []string {
	lines := strings.Split(body, "\n")
	var previews []string
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" {
			continue
		}
		previews = append(previews, trimmed)
		break
	}
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if trimmed == "" || !hasFailureMarker(trimmed) {
			continue
		}
		if len(previews) == 1 && previews[0] == trimmed {
			break
		}
		previews = append(previews, trimmed)
		break
	}
	return previews
}

// hasFailureMarker reports whether a line names a failure. The markers are
// shared with the context manifest so a line the summary calls a failure
// reads as one in the tombstone too.
func hasFailureMarker(line string) bool {
	for _, marker := range manifestFailureMarkers {
		if strings.Contains(line, marker) {
			return true
		}
	}
	return false
}

// tombstoneLineCount counts the lines of a body, ignoring the empty tail a
// trailing newline leaves behind.
func tombstoneLineCount(body string) int {
	body = strings.TrimRight(body, "\n")
	if body == "" {
		return 0
	}
	return strings.Count(body, "\n") + 1
}

// elideToolResults replaces eligible tool results with tombstones and returns
// the provider-facing parts keyed by tool call id. It is the single place the
// policy is applied, so shadow mode and on mode cannot diverge in what they
// decide; shadow mode only differs in what it sends.
func elideToolResults(recorded []recordedToolResult, policy elidePolicy, exempt map[string]struct{}) map[string][]fantasy.MessagePart {
	results := make([]message.ToolResult, len(recorded))
	for i, r := range recorded {
		results[i] = r.Result
	}

	byCall := make(map[string][]fantasy.MessagePart, len(recorded))
	for i, r := range recorded {
		part := r.Result.ToAIToolResultPart()
		if shouldElide(results, i, policy, exempt) {
			switch policy.Mode {
			case elideShadow:
				slog.Info(
					"Would elide tool result",
					"tool", r.Result.Name,
					"bytes", len(r.Result.Content),
					"tool_call_id", r.Result.ToolCallID,
					"saved_bytes", len(r.Result.Content),
				)
			case elideOn:
				elided := r.Result
				elided.Content = renderTombstone(r.Result, r.At)
				elided.Data = ""
				part = elided.ToAIToolResultPart()
			}
		}
		byCall[r.Result.ToolCallID] = append(byCall[r.Result.ToolCallID], part)
	}
	return byCall
}

// recordedToolResult pairs a tool result with the time the message carrying it
// was recorded, which is the tombstone's staleness signal. The two travel
// together because every caller has both and a bare []int64 parallel to the
// results is easy to misalign.
type recordedToolResult struct {
	Result message.ToolResult
	At     int64
}

// elideExemptKey is the context key carrying the current turn's tool-result
// ids into a compaction. A context is used rather than another parameter on
// [sessionAgent.Summarize] because it is a per-turn detail that only the
// prompt builder reads; the same reason the tools carry their session id
// this way.
type elideExemptKey struct{}

// withElideExempt attaches the turn's own tool-result ids to ctx.
func withElideExempt(ctx context.Context, ids map[string]struct{}) context.Context {
	if len(ids) == 0 {
		return ctx
	}
	return context.WithValue(ctx, elideExemptKey{}, ids)
}

// elideExemptFrom returns the turn-local exemption carried by ctx, or nil
// when none is attached.
func elideExemptFrom(ctx context.Context) map[string]struct{} {
	ids, _ := ctx.Value(elideExemptKey{}).(map[string]struct{})
	return ids
}
