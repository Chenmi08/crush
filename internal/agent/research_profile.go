package agent

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"slices"
	"time"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/permission"
)

const (
	// researchSessionTitle is the title given to research sub-sessions.
	researchSessionTitle = "Fetch Analysis"

	// researchExaBudget bounds how many Exa requests a single research
	// invocation may spend. Rate is not the concern — the shared limiter
	// queues and paces calls — so this is a ceiling on how long one
	// delegation may run and how much context it accumulates. It is set
	// well above a thorough run so it only stops a delegation that is
	// clearly looping.
	researchExaBudget = 32
)

// researchSearchToolNames are the built-in backend's tools the research
// sub-agent may call directly. web_fetch_exa is deliberately absent: the
// routed web_fetch tool calls it internally once it has decided a URL is
// safe to send upstream, so the sub-agent cannot bypass that check.
var researchSearchToolNames = []string{"web_search_exa", "web_search_advanced_exa"}

// researchRequest describes one research delegation.
type researchRequest struct {
	Prompt string
	URL    string
}

// newSubAgentHTTPClient returns the HTTP client shared by research tools.
func newSubAgentHTTPClient() *http.Client {
	transport := http.DefaultTransport.(*http.Transport).Clone()
	transport.MaxIdleConns = 100
	transport.MaxIdleConnsPerHost = 10
	transport.IdleConnTimeout = 90 * time.Second

	return &http.Client{
		Timeout:   30 * time.Second,
		Transport: transport,
	}
}

// runResearchProfile assembles a research sub-agent and runs it in its own
// session, returning a cited summary to the calling agent.
//
// The tool set is rebuilt on every invocation rather than when the agent is
// built, so a freshly connected MCP server is picked up immediately.
func (c *coordinator) runResearchProfile(ctx context.Context, call fantasy.ToolCall, toolName string, req researchRequest) (fantasy.ToolResponse, error) {
	sessionID := tools.GetSessionFromContext(ctx)
	if sessionID == "" {
		return fantasy.ToolResponse{}, errors.New("session id missing from context")
	}
	agentMessageID := tools.GetMessageFromContext(ctx)
	if agentMessageID == "" {
		return fantasy.ToolResponse{}, errors.New("agent message id missing from context")
	}

	description := "Search the web and analyze results"
	if req.URL != "" {
		description = fmt.Sprintf("Fetch and analyze content from URL: %s", req.URL)
	}

	approved, err := c.permissions.Request(
		ctx,
		permission.CreatePermissionRequest{
			SessionID:   sessionID,
			Path:        c.cfg.WorkingDir(),
			ToolCallID:  call.ID,
			ToolName:    toolName,
			Action:      "fetch",
			Description: description,
			Params: tools.AgentPermissionsParams{
				URL:    req.URL,
				Prompt: req.Prompt,
			},
		},
	)
	if err != nil {
		return fantasy.ToolResponse{}, err
	}
	if !approved {
		return tools.NewPermissionDeniedResponse(), nil
	}

	client := newSubAgentHTTPClient()

	tmpDir, err := os.MkdirTemp(c.cfg.Config().Options.DataDirectory, "crush-research-*")
	if err != nil {
		return fantasy.NewTextErrorResponse(fmt.Sprintf("Failed to create temporary directory: %s", err)), nil
	}
	defer os.RemoveAll(tmpDir)

	budget := tools.NewExaBudget(researchExaBudget)

	promptTemplate, err := researchPrompt(prompt.WithWorkingDir(tmpDir))
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("error creating prompt: %w", err)
	}

	_, small, err := c.buildAgentModels(ctx, true)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("error building models: %w", err)
	}

	systemPrompt, err := promptTemplate.Build(ctx, small.Model.Provider(), small.Model.Model(), c.cfg)
	if err != nil {
		return fantasy.ToolResponse{}, fmt.Errorf("error building system prompt: %w", err)
	}

	providerCfg, ok := c.cfg.Config().Providers.Get(small.ModelCfg.Provider)
	if !ok {
		return fantasy.ToolResponse{}, errors.New("small model provider not configured")
	}

	agent := NewSessionAgent(SessionAgentOptions{
		// Research is search plus summarisation, so the small model is
		// enough for both slots.
		LargeModel:           small,
		SmallModel:           small,
		SystemPromptPrefix:   providerCfg.SystemPromptPrefix,
		SystemPrompt:         systemPrompt,
		DisableAutoSummarize: c.cfg.Config().Options.DisableAutoSummarize,
		IsYolo:               c.permissions.SkipRequests(),
		Sessions:             c.sessions,
		Messages:             c.messages,
		Tools:                c.researchTools(ctx, tmpDir, client, budget),
	})

	return c.runSubAgent(ctx, subAgentParams{
		Agent:          agent,
		SessionID:      sessionID,
		AgentMessageID: agentMessageID,
		ToolCallID:     call.ID,
		Prompt:         buildResearchPrompt(req),
		SessionTitle:   researchSessionTitle,
		SessionSetup: func(sessionID string) {
			c.permissions.AutoApproveSession(sessionID)
		},
	})
}

// researchTools assembles the research sub-agent's tool set.
//
// The built-in search backend is preferred. DuckDuckGo search and the direct
// fetcher are registered only when that backend is unreachable, so the model
// never has to choose between two near-identical tools.
func (c *coordinator) researchTools(ctx context.Context, tmpDir string, client *http.Client, budget *tools.ExaBudget) []fantasy.AgentTool {
	base := []fantasy.AgentTool{
		tools.NewGlobTool(tmpDir, c.cfg.Config().Tools.Glob),
		tools.NewGrepTool(tmpDir, c.cfg.Config().Tools.Grep),
		tools.NewSourcegraphTool(client),
		tools.NewViewTool(c.lspManager, c.permissions, c.filetracker, nil, tmpDir),
	}

	exaTools := c.builtinSearchTools(ctx)
	if len(exaTools) == 0 {
		// The backend is unreachable, so the fallback fetcher gets no
		// configuration and reads every page directly rather than retrying
		// an endpoint that just failed.
		fallback := []fantasy.AgentTool{
			tools.NewWebSearchTool(client),
			tools.NewWebFetchTool(nil, tmpDir, client, nil),
		}
		return append(fallback, base...)
	}

	// The backend can connect and still fail per call: credits exhausted,
	// throttling that outlasts the retries, a dropped connection. Attach
	// DuckDuckGo as a per-call fallback so the sub-agent degrades instead
	// of receiving an error it cannot act on. The fallback is not
	// registered as a tool, so the model still sees one search tool.
	fallback := tools.NewExaSearchFallback(client)
	searchTools := make([]fantasy.AgentTool, 0, len(exaTools))
	for _, tool := range exaTools {
		tool.SetFallback(fallback)
		searchTools = append(searchTools, tool)
	}

	research := tools.WithExaBudget(budget, researchSearchToolNames, searchTools)
	research = append(research, tools.NewWebFetchTool(c.cfg, tmpDir, client, budget))
	return append(research, base...)
}

// builtinSearchTools returns the built-in backend's search tools, or nil when
// the backend cannot be reached — in which case the caller falls back to
// DuckDuckGo. web_fetch_exa is excluded on purpose: the routed web_fetch tool
// calls it internally, so the sub-agent cannot bypass the internal-address
// check by fetching a page itself.
func (c *coordinator) builtinSearchTools(ctx context.Context) []*tools.Tool {
	all, err := tools.BuiltinMCPTools(ctx, c.permissions, c.cfg, c.cfg.WorkingDir(), mcp.ExaServerName)
	if err != nil {
		slog.Debug("Built-in search backend unavailable", "error", err)
		return nil
	}

	var out []*tools.Tool
	for _, tool := range all {
		if slices.Contains(researchSearchToolNames, tool.MCPToolName()) {
			out = append(out, tool)
		}
	}
	return out
}

// buildResearchPrompt turns a research request into the sub-agent's first
// user message.
func buildResearchPrompt(req researchRequest) string {
	if req.URL != "" {
		return fmt.Sprintf(
			"%s\n\nRead this page with the web_fetch tool, then answer from its contents: %s",
			req.Prompt, req.URL,
		)
	}

	return fmt.Sprintf(
		"%s\n\nSearch with the web search tool in your list to find relevant pages. "+
			"Break the question into focused searches, then read the most relevant pages with web_fetch. "+
			"Batch every URL you need into a single web_fetch call: one call costs one request no matter how many URLs it carries.",
		req.Prompt,
	)
}
