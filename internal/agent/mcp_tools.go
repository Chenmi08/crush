package agent

import (
	"log/slog"
	"slices"

	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
)

// allowedMCPTool reports whether an agent may use the given MCP tool.
func allowedMCPTool(agent config.Agent, tool *tools.Tool) bool {
	return mcpToolAllowed(agent.AllowAllMCP, agent.AllowedMCP, tool.MCP(), tool.MCPToolName())
}

// mcpToolAllowed implements the MCP access policy.
//
// The policy is deliberately fail-closed:
//
//   - allowAll grants every tool from every server.
//   - Otherwise allowed is an allowlist keyed by server name, and the
//     zero value grants nothing.
//   - Within a server, a nil or empty tool list grants every tool.
func mcpToolAllowed(allowAll bool, allowed map[string][]string, server, tool string) bool {
	if allowAll {
		return true
	}

	toolsForServer, ok := allowed[server]
	if !ok {
		return false
	}
	return len(toolsForServer) == 0 || slices.Contains(toolsForServer, tool)
}

// mcpToolsFor filters the available MCP tools down to the ones the agent
// is allowed to use.
//
// Built-in servers are absent from this list by construction: they are never
// registered, so no policy here can expose them.
func mcpToolsFor(agent config.Agent, all []*tools.Tool) []*tools.Tool {
	out := make([]*tools.Tool, 0, len(all))
	for _, tool := range all {
		if allowedMCPTool(agent, tool) {
			out = append(out, tool)
			continue
		}
		slog.Debug("MCP tool not allowed", "tool", tool.Name(), "agent", agent.Name)
	}
	return out
}
