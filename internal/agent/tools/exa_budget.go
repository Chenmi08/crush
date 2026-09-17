package tools

import (
	"context"
	"fmt"
	"slices"
	"sync"

	"charm.land/fantasy"
)

// ExaBudget bounds how many Exa requests a single research invocation may
// spend. One budget is shared by every tool that consumes an Exa request,
// so a sub-agent cannot exceed what its caller allowed by spreading the
// work across tools.
//
// A budget of zero or less means unlimited.
type ExaBudget struct {
	mu   sync.Mutex
	max  int
	used int
}

// NewExaBudget returns a budget that allows max Exa requests.
func NewExaBudget(max int) *ExaBudget {
	return &ExaBudget{max: max}
}

// Spend reserves n requests, reporting an error when the budget is gone.
// The error text is fed back to the model, so it tells it what to do next.
func (b *ExaBudget) Spend(n int) error {
	if b == nil {
		return nil
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	if b.max > 0 && b.used+n > b.max {
		return fmt.Errorf(
			"Exa request budget exhausted (%d of %d used); stop searching and answer with the information you already have",
			b.used, b.max,
		)
	}
	b.used += n
	return nil
}

// Used reports how many requests have been spent.
func (b *ExaBudget) Used() int {
	if b == nil {
		return 0
	}

	b.mu.Lock()
	defer b.mu.Unlock()

	return b.used
}

// Max reports the configured limit, or zero when unlimited.
func (b *ExaBudget) Max() int {
	if b == nil {
		return 0
	}
	return b.max
}

// budgetedTool charges one Exa request before delegating to the wrapped
// tool.
type budgetedTool struct {
	fantasy.AgentTool
	budget *ExaBudget
}

func (t budgetedTool) Run(ctx context.Context, params fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if err := t.budget.Spend(1); err != nil {
		return fantasy.NewTextErrorResponse(err.Error()), nil
	}
	return t.AgentTool.Run(ctx, params)
}

// budgetedName resolves the name a budget matches a tool against. MCP tools
// wrap a server-local tool name but report it namespaced (for example
// "mcp_builtin-exa_web_search_exa"), so matching on Info().Name would never
// agree with a budget declared against "web_search_exa".
func budgetedName(tool fantasy.AgentTool) string {
	if mcpTool, ok := tool.(*Tool); ok {
		return mcpTool.MCPToolName()
	}
	return tool.Info().Name
}

// WithExaBudget wraps the named tools so that every call spends one Exa
// request from the shared budget. Tools that do not consume Exa requests
// are returned untouched.
func WithExaBudget(budget *ExaBudget, names []string, tools []fantasy.AgentTool) []fantasy.AgentTool {
	out := make([]fantasy.AgentTool, 0, len(tools))
	for _, tool := range tools {
		if slices.Contains(names, budgetedName(tool)) {
			out = append(out, budgetedTool{AgentTool: tool, budget: budget})
			continue
		}
		out = append(out, tool)
	}
	return out
}
