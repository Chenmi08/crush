package tools

import (
	"context"
	"fmt"
	"log/slog"
	"slices"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/permission"
)

// whitelistDockerTools contains Docker MCP tools that don't require permission.
var whitelistDockerTools = []string{
	"mcp_docker_mcp-find",
	"mcp_docker_mcp-add",
	"mcp_docker_mcp-remove",
	"mcp_docker_mcp-config-set",
	"mcp_docker_code-mode",
}

// GetMCPTools gets all the currently available MCP tools.
func GetMCPTools(permissions permission.Service, cfg *config.ConfigStore, wd string) []*Tool {
	var result []*Tool
	for mcpName, tools := range mcp.Tools() {
		for _, tool := range tools {
			result = append(result, &Tool{
				mcpName:     mcpName,
				tool:        tool,
				permissions: permissions,
				workingDir:  wd,
				cfg:         cfg,
			})
		}
	}
	return result
}

// BuiltinMCPTools returns the tools of a Crush-owned MCP server. Built-in
// servers never enter the shared registry, so asking for one by name is the
// only way to reach it — which is exactly what keeps it out of every agent's
// tool list.
func BuiltinMCPTools(ctx context.Context, permissions permission.Service, cfg *config.ConfigStore, wd, name string) ([]*Tool, error) {
	defs, err := mcp.BuiltinTools(ctx, cfg, name)
	if err != nil {
		return nil, err
	}

	out := make([]*Tool, 0, len(defs))
	for _, def := range defs {
		out = append(out, &Tool{
			mcpName:     name,
			tool:        def,
			permissions: permissions,
			workingDir:  wd,
			cfg:         cfg,
		})
	}
	return out, nil
}

// Tool is a tool from a MCP.
type Tool struct {
	mcpName         string
	tool            *mcp.Tool
	cfg             *config.ConfigStore
	permissions     permission.Service
	workingDir      string
	providerOptions fantasy.ProviderOptions
	// fallback runs when this MCP call fails; nil disables it.
	fallback fantasy.AgentTool
}

func (m *Tool) SetProviderOptions(opts fantasy.ProviderOptions) {
	m.providerOptions = opts
}

// SetFallback sets a tool to run when this MCP tool's call fails, so a
// failing backend can degrade instead of returning an error the model
// cannot act on. Nil disables it.
func (m *Tool) SetFallback(t fantasy.AgentTool) {
	m.fallback = t
}

func (m *Tool) ProviderOptions() fantasy.ProviderOptions {
	return m.providerOptions
}

func (m *Tool) Name() string {
	return fmt.Sprintf("mcp_%s_%s", m.mcpName, m.tool.Name)
}

func (m *Tool) MCP() string {
	return m.mcpName
}

func (m *Tool) MCPToolName() string {
	return m.tool.Name
}

func (m *Tool) Info() fantasy.ToolInfo {
	parameters := make(map[string]any)
	required := make([]string, 0)

	if input, ok := m.tool.InputSchema.(map[string]any); ok {
		if props, ok := input["properties"].(map[string]any); ok {
			parameters = props
		}
		if req, ok := input["required"].([]any); ok {
			// Convert []any -> []string when elements are strings
			for _, v := range req {
				if s, ok := v.(string); ok {
					required = append(required, s)
				}
			}
		} else if reqStr, ok := input["required"].([]string); ok {
			// Handle case where it's already []string
			required = reqStr
		}
	}

	return fantasy.ToolInfo{
		Name:        m.Name(),
		Description: m.tool.Description,
		Parameters:  parameters,
		Required:    required,
	}
}

func (m *Tool) Run(ctx context.Context, params fantasy.ToolCall) (fantasy.ToolResponse, error) {
	sessionID := GetSessionFromContext(ctx)
	if sessionID == "" {
		return fantasy.ToolResponse{}, fmt.Errorf("session ID is required for creating a new file")
	}

	// Skip permission for whitelisted Docker MCP tools.
	if !slices.Contains(whitelistDockerTools, params.Name) {
		permissionDescription := fmt.Sprintf("execute %s with the following parameters:", m.Info().Name)
		p, err := m.permissions.Request(
			ctx,
			permission.CreatePermissionRequest{
				SessionID:   sessionID,
				ToolCallID:  params.ID,
				Path:        m.workingDir,
				ToolName:    m.Info().Name,
				Action:      "execute",
				Description: permissionDescription,
				Params:      params.Input,
			},
		)
		if err != nil {
			return fantasy.ToolResponse{}, err
		}
		if !p {
			return NewPermissionDeniedResponse(), nil
		}
	}

	result, err := mcp.RunTool(ctx, m.cfg, m.mcpName, m.tool.Name, params.Input)
	if err != nil {
		if m.fallback == nil {
			return fantasy.NewTextErrorResponse(err.Error()), nil
		}
		return m.runFallback(ctx, params, err), nil
	}

	switch result.Type {
	case "image", "media":
		if !GetSupportsImagesFromContext(ctx) {
			modelName := GetModelNameFromContext(ctx)
			return fantasy.NewTextErrorResponse(fmt.Sprintf("This model (%s) does not support image data.", modelName)), nil
		}

		var response fantasy.ToolResponse
		if result.Type == "image" {
			response = fantasy.NewImageResponse(result.Data, result.MediaType)
		} else {
			response = fantasy.NewMediaResponse(result.Data, result.MediaType)
		}
		response.Content = result.Content
		return response, nil
	default:
		return fantasy.NewTextResponse(result.Content), nil
	}
}

// runFallback runs the fallback tool after an MCP call failed, for example
// when the upstream account is out of credit or throttling outlasts the
// retries. A fallback that fails is reported as a failure; on success its
// results are prefixed with a note naming what failed and what answered.
func (m *Tool) runFallback(ctx context.Context, params fantasy.ToolCall, mcpErr error) fantasy.ToolResponse {
	fallbackName := m.fallback.Info().Name
	slog.Debug(
		"MCP tool failed; falling back",
		"tool", m.Name(),
		"fallback", fallbackName,
		"error", mcpErr,
	)

	resp, err := m.fallback.Run(ctx, params)
	if err != nil {
		return fallbackFailedResponse(mcpErr, fallbackName, err.Error())
	}
	// Fallback tools report their own failures in-band, so a nil error is
	// not proof of success.
	if resp.IsError {
		return fallbackFailedResponse(mcpErr, fallbackName, resp.Content)
	}

	resp.Content = fmt.Sprintf(
		"[%s failed: %s; used %s instead.]\n\n%s",
		m.MCPToolName(), mcpErr, fallbackName, resp.Content,
	)
	return resp
}

// fallbackFailedResponse keeps the original failure alongside the
// fallback's, since the original is usually the one worth reporting.
func fallbackFailedResponse(mcpErr error, fallbackName, detail string) fantasy.ToolResponse {
	return fantasy.NewTextErrorResponse(
		fmt.Sprintf("%s; fallback %s failed: %s", mcpErr, fallbackName, detail),
	)
}
