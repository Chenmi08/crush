package tools

import (
	"context"
	"errors"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
)

// stubFallbackTool stands in for a fallback (for example the DuckDuckGo
// search tool) and records the call it received.
type stubFallbackTool struct {
	gotInput string
	response fantasy.ToolResponse
	err      error
}

func (s *stubFallbackTool) Info() fantasy.ToolInfo {
	return fantasy.ToolInfo{Name: WebSearchToolName, Description: "stub fallback", Parameters: map[string]any{}}
}

func (s *stubFallbackTool) Run(_ context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	s.gotInput = call.Input
	if s.err != nil {
		return fantasy.ToolResponse{}, s.err
	}
	return s.response, nil
}

func (s *stubFallbackTool) ProviderOptions() fantasy.ProviderOptions { return nil }

func (s *stubFallbackTool) SetProviderOptions(fantasy.ProviderOptions) {}

// newFailingMCPTool returns an MCP tool whose server is not configured, so
// mcp.RunTool fails without a network round trip.
func newFailingMCPTool(t *testing.T, fallback fantasy.AgentTool) *Tool {
	t.Helper()

	tool := &Tool{
		mcpName:     "not-a-server",
		tool:        &mcp.Tool{Name: "web_search_exa"},
		cfg:         config.NewTestStore(&config.Config{}),
		permissions: permission.NewPermissionService(t.TempDir(), true, []string{}),
		workingDir:  t.TempDir(),
	}
	if fallback != nil {
		tool.SetFallback(fallback)
	}
	return tool
}

func runFailingMCPTool(t *testing.T, tool *Tool) fantasy.ToolResponse {
	t.Helper()

	ctx := context.WithValue(t.Context(), SessionIDContextKey, "session-1")
	resp, err := tool.Run(ctx, fantasy.ToolCall{
		ID:    "call-1",
		Name:  "mcp_not-a-server_web_search_exa",
		Input: `{"query":"golang"}`,
	})
	require.NoError(t, err)
	return resp
}

func TestToolRunFallsBackOnMCPFailure(t *testing.T) {
	t.Parallel()

	fallback := &stubFallbackTool{response: fantasy.NewTextResponse("ddg results")}
	resp := runFailingMCPTool(t, newFailingMCPTool(t, fallback))

	require.Equal(t, `{"query":"golang"}`, fallback.gotInput)
	require.Contains(t, resp.Content, "web_search_exa failed")
	require.Contains(t, resp.Content, "used web_search instead")
	require.Contains(t, resp.Content, "ddg results")
}

func TestToolRunWithoutFallbackReportsError(t *testing.T) {
	t.Parallel()

	resp := runFailingMCPTool(t, newFailingMCPTool(t, nil))
	require.Contains(t, resp.Content, "not available")
}

func TestToolRunFallbackInBandFailureIsReported(t *testing.T) {
	t.Parallel()

	// The DuckDuckGo tool reports failures in its response rather than as
	// an error, so this is the failure mode the fallback must catch.
	fallback := &stubFallbackTool{response: fantasy.NewTextErrorResponse("Failed to search: ddg down")}
	resp := runFailingMCPTool(t, newFailingMCPTool(t, fallback))

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "fallback web_search failed")
	require.Contains(t, resp.Content, "ddg down")
}

func TestToolRunFallbackErrorIsReported(t *testing.T) {
	t.Parallel()

	fallback := &stubFallbackTool{err: errors.New("ddg down")}
	resp := runFailingMCPTool(t, newFailingMCPTool(t, fallback))

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "fallback web_search failed")
	require.Contains(t, resp.Content, "ddg down")
}
