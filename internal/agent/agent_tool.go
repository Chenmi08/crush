package agent

import (
	"context"
	_ "embed"
	"errors"
	"fmt"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/prompt"
	"github.com/charmbracelet/crush/internal/agent/tools"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed templates/agent_tool.md
var agentToolDescription string

// Agent profiles select what a delegated sub-agent is for.
const (
	// agentProfileContext searches the project for context and
	// implementation details. It is the default.
	agentProfileContext = "context"

	// agentProfileResearch searches the web and answers with citations.
	agentProfileResearch = "research"
)

type AgentParams struct {
	Prompt  string `json:"prompt" description:"The task for the agent to perform"`
	Profile string `json:"profile,omitempty" description:"Which agent to run: \"context\" (default) searches the codebase; \"research\" searches the web and answers with citations."`
	URL     string `json:"url,omitempty" description:"Research profile only: read this URL instead of searching the web."`
}

const (
	AgentToolName = tools.AgentToolName
)

func (c *coordinator) agentTool(ctx context.Context) (fantasy.AgentTool, error) {
	agentCfg, ok := c.cfg.Config().Agents[config.AgentTask]
	if !ok {
		return nil, errors.New("task agent not configured")
	}
	subAgentPrompt, err := taskPrompt(prompt.WithWorkingDir(c.cfg.WorkingDir()))
	if err != nil {
		return nil, err
	}

	contextAgent, err := c.buildAgent(ctx, subAgentPrompt, agentCfg, true)
	if err != nil {
		return nil, err
	}

	return fantasy.NewParallelAgentTool(
		AgentToolName,
		agentToolDescription,
		func(ctx context.Context, params AgentParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Prompt == "" {
				return fantasy.NewTextErrorResponse("prompt is required"), nil
			}

			switch params.Profile {
			case agentProfileResearch:
				return c.runResearchProfile(ctx, call, AgentToolName, researchRequest{
					Prompt: params.Prompt,
					URL:    params.URL,
				})
			case "", agentProfileContext:
				// Fall through to the codebase-searching agent below.
			default:
				return fantasy.NewTextErrorResponse(fmt.Sprintf(
					"unknown profile %q: use %q or %q",
					params.Profile, agentProfileContext, agentProfileResearch,
				)), nil
			}

			sessionID := tools.GetSessionFromContext(ctx)
			if sessionID == "" {
				return fantasy.ToolResponse{}, errors.New("session id missing from context")
			}

			agentMessageID := tools.GetMessageFromContext(ctx)
			if agentMessageID == "" {
				return fantasy.ToolResponse{}, errors.New("agent message id missing from context")
			}

			return c.runSubAgent(ctx, subAgentParams{
				Agent:          contextAgent,
				SessionID:      sessionID,
				AgentMessageID: agentMessageID,
				ToolCallID:     call.ID,
				Prompt:         params.Prompt,
				SessionTitle:   "New Agent Session",
			})
		},
	), nil
}
