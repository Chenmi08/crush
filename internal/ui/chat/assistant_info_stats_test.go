package chat

import (
	"strings"
	"testing"
	"time"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/charmbracelet/crush/internal/message"
	"github.com/charmbracelet/crush/internal/ui/styles"
	"github.com/charmbracelet/x/ansi"
	"github.com/stretchr/testify/require"
)

// testInfoConfig returns a config safe to call GetModel on during render.
func testInfoConfig() *config.Config {
	return &config.Config{Providers: csync.NewMap[string, config.ProviderConfig]()}
}

func TestAssistantInfoItemRendersTurnStats(t *testing.T) {
	t.Parallel()

	sty := styles.CharmtonePantera()
	msg := &message.Message{
		ID:    "info",
		Role:  message.Assistant,
		Parts: []message.ContentPart{message.Finish{Reason: message.FinishReasonEndTurn, Time: time.Now().Unix()}},
	}
	msg.SetStats(message.Stats{
		TTFTMillis:          800,
		TokensPerSecond:     42.3,
		CacheReadTokens:     12_420,
		CacheCreationTokens: 100,
		TotalPromptTokens:   13_500,
		OutputTokens:        1200,
	})
	item := NewAssistantInfoItem(&sty, msg, testInfoConfig(), time.Unix(0, 0))

	rendered := ansi.Strip(item.Render(80))

	require.Contains(t, rendered, "0.8s · 42.3 tok/s")
	require.Contains(t, rendered, "↑13.5K 92.00% · ↓1.2K")

	// Stats are ancillary metadata on their own line below the header,
	// not part of the model/provider line.
	header, stats, ok := strings.Cut(rendered, "\n")
	require.True(t, ok, "stats must render on a separate line")
	require.NotContains(t, header, "tok/s")
	require.Contains(t, stats, "tok/s")
}

func TestShouldShowAssistantInfoForStats(t *testing.T) {
	t.Parallel()

	stats := message.Stats{OutputTokens: 1200}

	// A truncated turn gets no EndTurn footer today, but its stats must
	// still be visible.
	maxTokens := &message.Message{
		Parts: []message.ContentPart{message.Finish{Reason: message.FinishReasonMaxTokens, Time: 1}},
	}
	maxTokens.SetStats(stats)
	require.True(t, ShouldShowAssistantInfo(maxTokens))

	// Stats without a finish part (mid-stream) never show a footer.
	streaming := &message.Message{}
	streaming.SetStats(stats)
	require.False(t, ShouldShowAssistantInfo(streaming))

	// No stats and no EndTurn: no footer.
	plain := &message.Message{
		Parts: []message.ContentPart{message.Finish{Reason: message.FinishReasonMaxTokens, Time: 1}},
	}
	require.False(t, ShouldShowAssistantInfo(plain))
}
