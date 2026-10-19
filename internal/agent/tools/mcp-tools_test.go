package tools

import (
	"bytes"
	"context"
	"errors"
	"image"
	"image/color"
	"image/png"
	"testing"

	"charm.land/fantasy"
	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
	"github.com/charmbracelet/crush/internal/sandbox"
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

// solidPNGBytes encodes a solid-color image as a PNG, for tests that need a
// small, real, decodable image payload.
func solidPNGBytes(t *testing.T, w, h int, c color.NRGBA) []byte {
	t.Helper()

	img := image.NewNRGBA(image.Rect(0, 0, w, h))
	for y := 0; y < h; y++ {
		for x := 0; x < w; x++ {
			img.SetNRGBA(x, y, c)
		}
	}
	var buf bytes.Buffer
	require.NoError(t, png.Encode(&buf, img))
	return buf.Bytes()
}

func TestImageResultResponseNormalizesOversizedImages(t *testing.T) {
	t.Parallel()

	// An MCP image result that exceeds the pixel ceiling is normalized
	// through the same strategy as the view tool: an image response, not a
	// "too large" error, with a coordinate-mapping hint.
	data := noisyPNGBytes(t, 2600, 2600)

	cfg := config.NewTestStore(&config.Config{})
	resp := imageResultResponse(mcp.ToolResult{
		Type:      "image",
		Content:   "here is the screenshot",
		Data:      data,
		MediaType: "image/png",
	}, cfg)

	require.False(t, resp.IsError)
	require.Equal(t, "image", resp.Type)
	require.NotEmpty(t, resp.Data)
	require.NotEqual(t, data, resp.Data)
	require.Contains(t, resp.Content, "here is the screenshot")
	require.Contains(t, resp.Content, "resized")
}

func TestImageResultResponsePassesThroughSmallImages(t *testing.T) {
	t.Parallel()

	data := solidPNGBytes(t, 40, 40, color.NRGBA{R: 10, G: 20, B: 30, A: 255})

	cfg := config.NewTestStore(&config.Config{})
	resp := imageResultResponse(mcp.ToolResult{
		Type:      "image",
		Data:      data,
		MediaType: "image/png",
	}, cfg)

	require.False(t, resp.IsError)
	require.Equal(t, "image", resp.Type)
	require.Equal(t, data, resp.Data, "small image must pass through unchanged")
}

func TestImageResultResponseErrorsOnHardCeiling(t *testing.T) {
	t.Parallel()

	data := solidPNGBytes(t, 8, 8, color.NRGBA{R: 1, G: 2, B: 3, A: 255})

	cfg := config.NewTestStore(&config.Config{Options: &config.Options{Image: &config.ImageOptions{
		MaxBytes: ptr(1),
	}}})
	resp := imageResultResponse(mcp.ToolResult{
		Type:      "image",
		Data:      data,
		MediaType: "image/png",
	}, cfg)

	require.True(t, resp.IsError)
	require.Contains(t, resp.Content, "too large")
}

func TestMediaResultResponseNormalizesImagePayload(t *testing.T) {
	t.Parallel()

	// A media result carrying image bytes must go through the same
	// normalize step, so it cannot bypass the pixel and byte ceilings.
	data := noisyPNGBytes(t, 2600, 2600)

	cfg := config.NewTestStore(&config.Config{})
	resp := mediaResultResponse(mcp.ToolResult{
		Type:      "media",
		Content:   "here is the screenshot",
		Data:      data,
		MediaType: "image/png",
	}, cfg)

	require.False(t, resp.IsError)
	require.Equal(t, "image", resp.Type)
	require.NotEqual(t, data, resp.Data)
	require.Contains(t, resp.Content, "resized")
}

func TestMediaResultResponsePassesAudioThrough(t *testing.T) {
	t.Parallel()

	// Non-image media payloads are untouched: audio keeps its media
	// response and its content.
	data := []byte("fake-audio-bytes")

	cfg := config.NewTestStore(&config.Config{})
	resp := mediaResultResponse(mcp.ToolResult{
		Type:      "media",
		Content:   "transcript follows",
		Data:      data,
		MediaType: "audio/wav",
	}, cfg)

	require.False(t, resp.IsError)
	require.Equal(t, "media", resp.Type)
	require.Equal(t, data, resp.Data)
	require.Equal(t, "transcript follows", resp.Content)
}

// TestMCPExecutePromptsUnlessDelegated verifies MCP execution asks for
// approval under a confined policy: MCP servers run outside the sandbox with
// the user's authority. danger-full-access delegates fully and asks nothing.
func TestMCPExecutePromptsUnlessDelegated(t *testing.T) {
	t.Parallel()

	workspace := t.TempDir()
	perms := &recordingPermissionService{Broker: pubsubBroker(), allow: true}
	tool := &Tool{
		mcpName:     "not-a-server",
		tool:        &mcp.Tool{Name: "web_search_exa"},
		cfg:         config.NewTestStore(&config.Config{}),
		permissions: perms,
		workingDir:  workspace,
	}
	call := fantasy.ToolCall{ID: "call-1", Name: tool.Info().Name, Input: `{"query":"golang"}`}

	ctx := WithSandboxPolicy(
		context.WithValue(t.Context(), SessionIDContextKey, "session-1"),
		tableSandboxPolicy(t, workspace),
	)
	_, _ = tool.Run(ctx, call)
	require.Equal(t, 1, perms.requestCount, "MCP execution must prompt under a confined policy")
	require.NotNil(t, perms.lastRequest)
	require.Equal(t, "execute", perms.lastRequest.Action)
	require.False(t, perms.lastRequest.Sandboxed,
		"MCP runs outside the sandbox; the prompt must say so")

	perms.requestCount = 0
	delegated := WithSandboxPolicy(ctx, &sandbox.Policy{Mode: sandbox.ModeDangerFullAccess})
	_, _ = tool.Run(delegated, call)
	require.Equal(t, 0, perms.requestCount,
		"danger-full-access delegates fully: MCP execution does not prompt")
}
