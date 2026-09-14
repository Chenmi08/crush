package agent

import (
	"time"

	"github.com/charmbracelet/crush/internal/message"
)

// turnStats accumulates usage statistics across every model step of a
// single user turn. The agent attaches the snapshot to the assistant
// message that ends the turn so the UI can render one stats block per
// turn instead of per step.
type turnStats struct {
	stats          message.Stats
	generationTime time.Duration
	steps          int
}

// addStep merges one finished step into the turn. Prompt and cache
// counters are summed so a turn's stats are the increment of the session
// totals.
func (t *turnStats) addStep(step stepMetrics) {
	// The first step's time to first token is what the user perceived as
	// the response latency; later steps run after tool execution.
	if t.steps == 0 {
		t.stats.TTFTMillis = step.stats.TTFTMillis
	}
	t.steps++
	t.stats.OutputTokens += step.stats.OutputTokens
	t.stats.CacheReadTokens += step.stats.CacheReadTokens
	t.stats.CacheCreationTokens += step.stats.CacheCreationTokens
	t.stats.TotalPromptTokens += step.stats.TotalPromptTokens
	t.stats.Estimated = t.stats.Estimated || step.stats.Estimated
	t.generationTime += step.window
}

// snapshot returns the aggregated per-turn stats. The generation speed is
// recomputed from the summed output tokens and generation window so it is
// a weighted average instead of a sum of per-step speeds. The cache hit
// rate is derived from the summed counters by message.Stats.
func (t *turnStats) snapshot() message.Stats {
	stats := t.stats
	if t.generationTime > 0 && stats.OutputTokens > 0 {
		stats.TokensPerSecond = float64(stats.OutputTokens) / t.generationTime.Seconds()
	}
	return stats
}
