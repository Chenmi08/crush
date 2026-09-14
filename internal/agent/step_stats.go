package agent

import (
	"time"

	"charm.land/fantasy"
	"github.com/charmbracelet/crush/internal/message"
)

// stepMetrics carries the statistics of a single model step and the
// generation window they were derived from. The window lets per-turn
// aggregation weight the generation speed by actual generation time
// instead of averaging the per-step speeds.
type stepMetrics struct {
	stats  message.Stats
	window time.Duration
}

// computeStepStats derives runtime statistics for a single model step from
// the stream callback timestamps: time to first token, generation speed,
// and cache usage. usage is the (possibly estimated) usage recorded for
// the step; streamUsage is the provider-reported usage observed at stream
// finish, which is preferred for the tokens-per-second calculation.
// estimated reports whether usage was approximated locally.
func computeStepStats(stepStart, firstTokenAt, streamEnd time.Time, streamUsage, usage fantasy.Usage, estimated bool) stepMetrics {
	metrics := stepMetrics{stats: message.Stats{
		Estimated:           estimated,
		CacheReadTokens:     usage.CacheReadTokens,
		CacheCreationTokens: usage.CacheCreationTokens,
		OutputTokens:        usage.OutputTokens,
	}}
	if promptTokens := usage.InputTokens + usage.CacheReadTokens + usage.CacheCreationTokens; promptTokens > 0 {
		metrics.stats.TotalPromptTokens = promptTokens
	}

	if firstTokenAt.IsZero() || streamEnd.IsZero() || streamEnd.Before(stepStart) {
		return metrics
	}

	if ttft := firstTokenAt.Sub(stepStart); ttft > 0 {
		metrics.stats.TTFTMillis = ttft.Milliseconds()
	}

	outputTokens := streamUsage.OutputTokens
	if outputTokens == 0 {
		outputTokens = usage.OutputTokens
	}
	if window := streamEnd.Sub(firstTokenAt); window > 0 {
		metrics.window = window
		if outputTokens > 0 {
			metrics.stats.TokensPerSecond = float64(outputTokens) / window.Seconds()
		}
	}

	return metrics
}
