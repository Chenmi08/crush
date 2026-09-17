package tools

import (
	"context"
	"testing"

	"charm.land/fantasy"
	sdkmcp "github.com/modelcontextprotocol/go-sdk/mcp"
	"github.com/stretchr/testify/require"
)

// stubTool is a minimal fantasy.AgentTool used to observe whether a call
// reached the underlying tool.
type stubTool struct {
	name string
	runs int
}

func (s *stubTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: s.name}
}

func (s *stubTool) Run(context.Context, fantasy.ToolCall) (fantasy.ToolResponse, error) {
	s.runs++
	return fantasy.NewTextResponse("ok"), nil
}

func (s *stubTool) ProviderOptions() fantasy.ProviderOptions   { return nil }
func (s *stubTool) SetProviderOptions(fantasy.ProviderOptions) {}

func TestExaBudgetUnlimited(t *testing.T) {
	t.Parallel()

	budget := NewExaBudget(0)
	for range 100 {
		require.NoError(t, budget.Spend(1))
	}
	require.Equal(t, 100, budget.Used())
	require.Equal(t, 0, budget.Max())
}

func TestExaBudgetStopsAtLimit(t *testing.T) {
	t.Parallel()

	budget := NewExaBudget(2)
	require.NoError(t, budget.Spend(1))
	require.NoError(t, budget.Spend(1))

	err := budget.Spend(1)
	require.Error(t, err)
	require.Contains(t, err.Error(), "budget exhausted")
	require.Equal(t, 2, budget.Used())
}

func TestExaBudgetNilIsUnlimited(t *testing.T) {
	t.Parallel()

	var budget *ExaBudget
	require.NoError(t, budget.Spend(5))
	require.Equal(t, 0, budget.Used())
}

func TestWithExaBudgetChargesOnlyNamedTools(t *testing.T) {
	t.Parallel()

	budget := NewExaBudget(2)
	search := &stubTool{name: "web_search_exa"}
	other := &stubTool{name: "glob"}

	wrapped := WithExaBudget(budget, []string{"web_search_exa"}, []fantasy.AgentTool{search, other})
	require.Len(t, wrapped, 2)

	ctx := context.Background()

	// The unbudgeted tool never spends.
	_, err := wrapped[1].Run(ctx, fantasy.ToolCall{})
	require.NoError(t, err)
	require.Equal(t, 0, budget.Used())
	require.Equal(t, 1, other.runs)

	_, err = wrapped[0].Run(ctx, fantasy.ToolCall{})
	require.NoError(t, err)
	_, err = wrapped[0].Run(ctx, fantasy.ToolCall{})
	require.NoError(t, err)
	require.Equal(t, 2, budget.Used())
	require.Equal(t, 2, search.runs)

	// The third call is refused with a response the model can act on, and
	// never reaches the underlying tool.
	resp, err := wrapped[0].Run(ctx, fantasy.ToolCall{})
	require.NoError(t, err)
	require.Contains(t, resp.Content, "budget exhausted")
	require.Equal(t, 2, search.runs)
}

// TestWithExaBudgetMatchesMCPToolNames guards against the namespaced name an
// MCP tool reports. A budget is declared against the server-local name
// ("web_search_exa"), but the tool's Info().Name is
// "mcp_builtin-exa_web_search_exa", so a naive match silently wraps nothing.
func TestWithExaBudgetMatchesMCPToolNames(t *testing.T) {
	t.Parallel()

	budget := NewExaBudget(1)
	require.NoError(t, budget.Spend(1))

	mcpTool := &Tool{
		mcpName: "builtin-exa",
		tool:    &sdkmcp.Tool{Name: "web_search_exa"},
	}

	wrapped := WithExaBudget(budget, []string{"web_search_exa"}, []fantasy.AgentTool{mcpTool})
	require.Len(t, wrapped, 1)

	// The budget is already gone, so the wrapped tool refuses the call
	// without ever reaching the MCP server.
	resp, err := wrapped[0].Run(context.Background(), fantasy.ToolCall{})
	require.NoError(t, err)
	require.Contains(t, resp.Content, "budget exhausted")
}

func TestWithExaBudgetSharedAcrossTools(t *testing.T) {
	t.Parallel()

	budget := NewExaBudget(2)
	first := &stubTool{name: "web_search_exa"}
	second := &stubTool{name: "web_search_advanced_exa"}

	wrapped := WithExaBudget(budget,
		[]string{"web_search_exa", "web_search_advanced_exa"},
		[]fantasy.AgentTool{first, second},
	)

	ctx := context.Background()
	_, err := wrapped[0].Run(ctx, fantasy.ToolCall{})
	require.NoError(t, err)
	_, err = wrapped[1].Run(ctx, fantasy.ToolCall{})
	require.NoError(t, err)

	// The budget is shared, so the third call is refused whichever tool
	// makes it.
	resp, err := wrapped[1].Run(ctx, fantasy.ToolCall{})
	require.NoError(t, err)
	require.Contains(t, resp.Content, "budget exhausted")
	require.Equal(t, 1, second.runs)
}
