package mcp

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"iter"
	"log/slog"
	"slices"
	"strings"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/csync"
	"github.com/modelcontextprotocol/go-sdk/mcp"
)

type Tool = mcp.Tool

// ToolResult represents the result of running an MCP tool.
type ToolResult struct {
	Type      string
	Content   string
	Data      []byte
	MediaType string
}

var allTools = csync.NewMap[string, []*Tool]()

// Tools returns all available MCP tools.
func Tools() iter.Seq2[string, []*Tool] {
	return allTools.Seq2()
}

// RunTool runs an MCP tool with the given input parameters.
func RunTool(ctx context.Context, cfg *config.ConfigStore, name, toolName string, input string) (ToolResult, error) {
	var args map[string]any
	if err := json.Unmarshal([]byte(input), &args); err != nil {
		return ToolResult{}, fmt.Errorf("error parsing parameters: %w", err)
	}

	c, err := getOrRenewClient(ctx, cfg, name)
	if err != nil {
		return ToolResult{}, err
	}
	result, err := callTool(ctx, limiterForServer(cfg, name), name, c, toolName, args)
	if err != nil {
		return ToolResult{}, err
	}

	return toolResultFromCall(result)
}

// toolResultFromCall converts a successful MCP response into a ToolResult.
// A response with IsError set is a tool-originated failure (an upstream
// 402, a 429 that outlasted the retries, and the like) carried in the
// content rather than as a transport error; it is returned as an error so
// callers can fall back instead of feeding the model a dead end.
func toolResultFromCall(result *mcp.CallToolResult) (ToolResult, error) {
	if len(result.Content) == 0 {
		if result.IsError {
			return ToolResult{}, errors.New("tool reported an error")
		}
		return ToolResult{Type: "text", Content: ""}, nil
	}

	var textParts []string
	var imageData []byte
	var imageMimeType string
	var audioData []byte
	var audioMimeType string

	for _, v := range result.Content {
		switch content := v.(type) {
		case *mcp.TextContent:
			textParts = append(textParts, content.Text)
		case *mcp.ImageContent:
			if imageData == nil {
				imageData = content.Data
				imageMimeType = content.MIMEType
			}
		case *mcp.AudioContent:
			if audioData == nil {
				audioData = content.Data
				audioMimeType = content.MIMEType
			}
		default:
			textParts = append(textParts, fmt.Sprintf("%v", v))
		}
	}

	textContent := strings.Join(textParts, "\n")
	if result.IsError {
		if textContent == "" {
			textContent = "tool reported an error"
		}
		return ToolResult{}, errors.New(textContent)
	}

	// We need to make sure the data is base64
	// when using something like docker + playwright the data was not returned correctly.
	if imageData != nil {
		return ToolResult{
			Type:      "image",
			Content:   textContent,
			Data:      ensureRawBytes(imageData),
			MediaType: imageMimeType,
		}, nil
	}

	if audioData != nil {
		return ToolResult{
			Type:      "media",
			Content:   textContent,
			Data:      ensureRawBytes(audioData),
			MediaType: audioMimeType,
		}, nil
	}

	return ToolResult{
		Type:    "text",
		Content: textContent,
	}, nil
}

// limiterForServer resolves the shared limiter configured for an MCP
// server, or nil when the server has no rate limit.
func limiterForServer(cfg *config.ConfigStore, name string) *serverLimiter {
	// Built-in servers carry their own configuration rather than reading it
	// from the user's config.
	if srv, ok := builtinEffectiveConfig(name); ok {
		return limiterFor(name, srv.RateLimit, srv.RateBurst)
	}

	if cfg == nil || cfg.Config() == nil {
		return nil
	}
	srv, ok := cfg.Config().MCP[name]
	if !ok {
		return nil
	}
	return limiterFor(name, srv.RateLimit, srv.RateBurst)
}

// toolCaller is the part of ClientSession that callTool needs. Depending on
// the interface rather than the concrete session keeps the retry policy
// testable without a live MCP server.
type toolCaller interface {
	CallTool(ctx context.Context, params *mcp.CallToolParams) (*mcp.CallToolResult, error)
}

// callTool sends one tools/call, serialising through the server's shared
// limiter when one is configured.
//
// A throttled response always backs off and retries. With a limiter the
// whole server enters cooldown, so everything queued behind this call waits
// with it; without one the call still sleeps, because an upstream rate limit
// should cost latency rather than fail the request outright. Configuring a
// rate limit must not be a prerequisite for surviving throttling.
func callTool(ctx context.Context, lim *serverLimiter, server string, c toolCaller, toolName string, args map[string]any) (*mcp.CallToolResult, error) {
	const maxAttempts = 3

	var lastErr error
	for attempt := range maxAttempts {
		if lim != nil {
			if err := lim.acquire(ctx); err != nil {
				return nil, err
			}
		}

		result, err := c.CallTool(ctx, &mcp.CallToolParams{
			Name:      toolName,
			Arguments: args,
		})
		if err == nil {
			return result, nil
		}
		lastErr = err

		if !isRateLimited(err) {
			return nil, err
		}

		slog.Debug(
			"MCP server throttled; backing off",
			"server", server,
			"tool", toolName,
			"attempt", attempt+1,
			"error", err,
		)

		// There is nothing left to retry after the final attempt, so do not
		// make this caller — or, with a limiter, everyone queued behind it —
		// wait for a backoff that will never be used.
		if attempt == maxAttempts-1 {
			break
		}

		if lim != nil {
			lim.cooldown(defaultCooldown)
			continue
		}

		if err := sleepCtx(ctx, defaultCooldown); err != nil {
			return nil, err
		}
	}

	return nil, lastErr
}

// RefreshTools gets the updated list of tools from the MCP and updates the
// global state.
func RefreshTools(ctx context.Context, cfg *config.ConfigStore, name string) {
	// Serialize with session renewal so the registered session can't be
	// swapped between the Get and the state update below — a stale error
	// transition would otherwise tear down the healthy replacement.
	mu := renewLock(name)
	mu.Lock()
	defer mu.Unlock()

	session, ok := sessions.Get(name)
	if !ok {
		slog.Warn("Refresh tools: no session", "name", name)
		return
	}

	tools, err := getTools(ctx, session)
	if err != nil {
		updateState(name, StateError, err, session, Counts{})
		return
	}

	toolCount := updateTools(cfg, name, tools)

	prev, _ := states.Get(name)
	prev.Counts.Tools = toolCount
	updateState(name, StateConnected, nil, session, prev.Counts)
}

// registerSessionTools lists the tools a live session exposes and writes them
// into the shared registry, returning the number registered after any
// configured allow/deny filtering. It is the single seam through which a
// (re)connected session's tools enter the registry, so both the initial
// connect and a lazy renew repopulate the tool list the agent sends to the LLM
// instead of leaving it empty.
func registerSessionTools(ctx context.Context, cfg *config.ConfigStore, name string, sess *ClientSession) (int, error) {
	tools, err := getTools(ctx, sess)
	if err != nil {
		return 0, err
	}
	return updateTools(cfg, name, tools), nil
}

func getTools(ctx context.Context, session *ClientSession) ([]*Tool, error) {
	// Always call ListTools to get the actual available tools.
	// The InitializeResult Capabilities.Tools field may be an empty object {},
	// which is valid per MCP spec, but we still need to call ListTools to discover tools.
	result, err := session.ListTools(ctx, &mcp.ListToolsParams{})
	if err != nil {
		return nil, err
	}
	return result.Tools, nil
}

func updateTools(cfg *config.ConfigStore, name string, tools []*Tool) int {
	mcpCfg, ok := cfg.Config().MCP[name]
	if ok {
		tools = filterTools(mcpCfg, tools)
	}
	if len(tools) == 0 {
		allTools.Del(name)
		return 0
	}
	allTools.Set(name, tools)
	return len(tools)
}

// filterTools filters tools based on enabled_tools (allow list) and
// disabled_tools (deny list) from the MCP config.
func filterTools(mcpCfg config.MCPConfig, tools []*Tool) []*Tool {
	if len(mcpCfg.EnabledTools) > 0 {
		filtered := make([]*Tool, 0, len(mcpCfg.EnabledTools))
		for _, tool := range tools {
			if slices.Contains(mcpCfg.EnabledTools, tool.Name) {
				filtered = append(filtered, tool)
			}
		}
		tools = filtered
	}

	if len(mcpCfg.DisabledTools) > 0 {
		filtered := make([]*Tool, 0, len(tools))
		for _, tool := range tools {
			if !slices.Contains(mcpCfg.DisabledTools, tool.Name) {
				filtered = append(filtered, tool)
			}
		}
		tools = filtered
	}

	return tools
}

// ensureRawBytes normalizes MCP media data into raw binary bytes.
//
// The MCP Go SDK's json.Unmarshal normally base64-decodes
// ImageContent.Data into raw bytes automatically. However, some MCP
// transports (notably Docker over stdio) can deliver data in
// unexpected formats. This function handles both cases:
//
//   - If data looks like a valid base64 string (ASCII-only, decodable)
//     it is decoded and the raw bytes are returned.
//   - If data is already raw binary (contains bytes > 127) it is
//     returned as-is.
func ensureRawBytes(data []byte) []byte {
	if len(data) == 0 {
		return data
	}

	normalized := normalizeBase64Input(data)
	if decoded, ok := decodeBase64(normalized); ok {
		return decoded
	}

	// Already raw binary — return unchanged.
	return data
}

func normalizeBase64Input(data []byte) []byte {
	normalized := strings.Join(strings.Fields(string(data)), "")
	return []byte(normalized)
}

func decodeBase64(data []byte) ([]byte, bool) {
	if len(data) == 0 {
		return data, true
	}

	for _, b := range data {
		if b > 127 {
			return nil, false
		}
	}

	s := string(data)
	decoded, err := base64.StdEncoding.DecodeString(s)
	if err == nil {
		return decoded, true
	}
	decoded, err = base64.RawStdEncoding.DecodeString(s)
	if err == nil {
		return decoded, true
	}
	return nil, false
}
