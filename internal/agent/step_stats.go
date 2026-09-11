package agent

import (
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/session"
)

// computeStepStats derives runtime statistics for a single model step from
// the stream callback timestamps: time to first token, generation speed,
// and cache usage. usage is the (possibly estimated) usage recorded for
// the step; streamUsage is the provider-reported usage observed at stream
// finish, which is preferred for the tokens-per-second calculation.
func computeStepStats(stepStart, firstTokenAt, streamEnd time.Time, streamUsage, usage fantasy.Usage) session.StepStats {
	stats := session.StepStats{
		CacheReadTokens:     usage.CacheReadTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		OutputTokens:        usage.OutputTokens,
	}
	if promptTokens := usage.InputTokens + usage.CacheReadTokens + usage.CacheCreationTokens; promptTokens > 0 {
		stats.TotalPromptTokens = promptTokens
		stats.CacheHitRate = float64(usage.CacheReadTokens) / float64(promptTokens)
	}

	if firstTokenAt.IsZero() || streamEnd.IsZero() || streamEnd.Before(stepStart) {
		return stats
	}

	if ttft := firstTokenAt.Sub(stepStart); ttft > 0 {
		stats.TTFT = ttft
	}

	outputTokens := streamUsage.OutputTokens
	if outputTokens == 0 {
		outputTokens = usage.OutputTokens
	}
	if window := streamEnd.Sub(firstTokenAt); window > 0 && outputTokens > 0 {
		stats.TokensPerSecond = float64(outputTokens) / window.Seconds()
	}

	return stats
}
