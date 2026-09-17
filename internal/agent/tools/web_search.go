package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"html/template"
	"log/slog"
	"net/http"
	"time"

	"charm.land/fantasy"
)

//go:embed web_search.md.tpl
var webSearchDescriptionTmpl []byte

var webSearchDescriptionTpl = template.Must(
	template.New("webSearchDescription").
		Parse(string(webSearchDescriptionTmpl)),
)

// NewWebSearchTool creates a web search tool for sub-agents (no permissions needed).
func NewWebSearchTool(client *http.Client) fantasy.AgentTool {
	if client == nil {
		transport := http.DefaultTransport.(*http.Transport).Clone()
		transport.MaxIdleConns = 100
		transport.MaxIdleConnsPerHost = 10
		transport.IdleConnTimeout = 90 * time.Second

		client = &http.Client{
			Timeout:   30 * time.Second,
			Transport: transport,
		}
	}

	return fantasy.NewParallelAgentTool(
		WebSearchToolName,
		renderToolDescription(webSearchDescriptionTpl),
		func(ctx context.Context, params WebSearchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if params.Query == "" {
				return fantasy.NewTextErrorResponse("query is required"), nil
			}

			maxResults := params.MaxResults
			if maxResults <= 0 {
				maxResults = 10
			}
			if maxResults > 20 {
				maxResults = 20
			}

			maybeDelaySearch()
			results, err := searchDuckDuckGo(ctx, client, params.Query, maxResults)
			slog.Debug("Web search completed", "query", params.Query, "results", len(results), "err", err)
			if err != nil {
				return fantasy.NewTextErrorResponse("Failed to search: " + err.Error()), nil
			}

			return fantasy.NewTextResponse(formatSearchResults(results)), nil
		},
	)
}

// NewExaSearchFallback returns the DuckDuckGo search tool adapted to the
// parameter names Exa's search tools use, for use as a call-time fallback
// when the Exa backend fails.
func NewExaSearchFallback(client *http.Client) fantasy.AgentTool {
	return remapInputTool{
		AgentTool: NewWebSearchTool(client),
		remap:     exaSearchParamsToWebSearch,
	}
}

// remapInputTool rewrites a tool call's input before delegating.
type remapInputTool struct {
	fantasy.AgentTool
	remap func(string) string
}

func (t remapInputTool) Run(ctx context.Context, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
	if t.remap != nil {
		call.Input = t.remap(call.Input)
	}
	return t.AgentTool.Run(ctx, call)
}

// exaSearchParamsToWebSearch maps the Exa search schema onto DuckDuckGo's:
// the query passes through, numResults becomes max_results, and filters
// with no DuckDuckGo equivalent are dropped. Input that cannot be parsed,
// or that carries no query, is returned unchanged so the fallback reports
// its own error.
func exaSearchParamsToWebSearch(input string) string {
	var params struct {
		Query      string `json:"query"`
		NumResults int    `json:"numResults"`
	}
	if err := json.Unmarshal([]byte(input), &params); err != nil || params.Query == "" {
		return input
	}

	mapped := map[string]any{"query": params.Query}
	if params.NumResults > 0 {
		mapped["max_results"] = params.NumResults
	}
	out, err := json.Marshal(mapped)
	if err != nil {
		return input
	}
	return string(out)
}
