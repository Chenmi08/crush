package agent

import (
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
)

// TestResearchToolsUseDDGWhenExaDisabled verifies the exa switch keeps
// research entirely off the built-in backend: DuckDuckGo search and the
// local-only fetcher replace it.
func TestResearchToolsUseDDGWhenExaDisabled(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{
		Options: &config.Options{DisableExa: true},
	})
	c := &coordinator{
		cfg:         cfg,
		permissions: permission.NewPermissionService(t.TempDir(), true, []string{}),
	}

	agentTools := c.researchTools(t.Context(), t.TempDir(), newSubAgentHTTPClient(), tools.NewExaBudget(1))

	names := make([]string, 0, len(agentTools))
	for _, tool := range agentTools {
		names = append(names, tool.Info().Name)
	}
	require.Contains(t, names, tools.WebSearchToolName)
	require.Contains(t, names, tools.WebFetchToolName)
	for _, name := range names {
		require.NotContains(t, name, mcp.ExaServerName)
	}
}
