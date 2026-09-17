package tools

import (
	"context"
	_ "embed"
	"encoding/json"
	"fmt"
	"html/template"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"

	"charm.land/fantasy"

	"github.com/charmbracelet/crush/internal/agent/tools/mcp"
	"github.com/charmbracelet/crush/internal/config"
)

//go:embed web_fetch.md.tpl
var webFetchDescriptionTmpl []byte

var webFetchDescriptionTpl = template.Must(
	template.New("webFetchDescription").
		Parse(string(webFetchDescriptionTmpl)),
)

// exaFetchToolName is Exa's MCP fetch tool. The routed fetch calls it
// internally so the model only ever sees one fetch tool.
const exaFetchToolName = "web_fetch_exa"

// defaultExaFetchCharacters bounds how much of each page Exa returns. Exa's
// own default is 3000, which is too small for most documentation.
const defaultExaFetchCharacters = 20000

// IsInternalURL reports whether a URL targets a loopback, private, or
// otherwise local address. Such URLs must never be handed to a third-party
// service, so they are always fetched directly.
func IsInternalURL(rawURL string) bool {
	parsed, err := url.Parse(rawURL)
	if err != nil {
		return false
	}

	host := strings.ToLower(parsed.Hostname())
	if host == "" {
		return false
	}

	if host == "localhost" || strings.HasSuffix(host, ".localhost") ||
		strings.HasSuffix(host, ".local") || strings.HasSuffix(host, ".internal") ||
		strings.HasSuffix(host, ".lan") {
		return true
	}

	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}

	return ip.IsLoopback() || ip.IsPrivate() || ip.IsLinkLocalUnicast() ||
		ip.IsLinkLocalMulticast() || ip.IsUnspecified()
}

// NewWebFetchTool creates the research sub-agent's fetch tool.
//
// External URLs are read through Exa's web_fetch_exa, which returns content
// for client-rendered pages that a plain HTTP GET only sees as an empty
// shell, and which bills a whole batch of URLs as one request. Internal
// URLs always go through the local fetcher and never leave the machine.
// A nil cfg means the built-in backend is unavailable, which is the
// research fallback: every URL is then read locally.
func NewWebFetchTool(cfg *config.ConfigStore, workingDir string, client *http.Client, budget *ExaBudget) fantasy.AgentTool {
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
		WebFetchToolName,
		renderToolDescription(webFetchDescriptionTpl),
		func(ctx context.Context, params WebFetchParams, call fantasy.ToolCall) (fantasy.ToolResponse, error) {
			if len(params.URLs) == 0 {
				return fantasy.NewTextErrorResponse("urls is required"), nil
			}

			maxCharacters := params.MaxCharacters
			if maxCharacters <= 0 {
				maxCharacters = defaultExaFetchCharacters
			}

			var internal, external []string
			for _, rawURL := range params.URLs {
				if IsInternalURL(rawURL) {
					internal = append(internal, rawURL)
					continue
				}
				external = append(external, rawURL)
			}

			var out strings.Builder

			for _, rawURL := range internal {
				writeLocalFetch(ctx, &out, workingDir, client, rawURL)
			}

			if len(external) > 0 {
				// Do not silently substitute a local fetch when the budget
				// is gone: the caller asked us to stop spending.
				if err := budget.Spend(1); err != nil {
					fmt.Fprintf(&out, "[%s]\n\n", err)
				} else if !writeExaFetch(ctx, cfg, &out, external, maxCharacters) {
					// The built-in backend is unreachable or refused the
					// batch, so read the pages directly instead.
					for _, rawURL := range external {
						writeLocalFetch(ctx, &out, workingDir, client, rawURL)
					}
				}
			}

			if out.Len() == 0 {
				return fantasy.NewTextErrorResponse("no content was fetched"), nil
			}
			return fantasy.NewTextResponse(out.String()), nil
		},
	)
}

// writeExaFetch reads a batch of external URLs in one request to the
// built-in search backend. It reports whether content was produced.
func writeExaFetch(ctx context.Context, cfg *config.ConfigStore, out *strings.Builder, urls []string, maxCharacters int) bool {
	if cfg == nil {
		return false
	}

	payload, err := json.Marshal(map[string]any{
		"urls":          urls,
		"maxCharacters": maxCharacters,
	})
	if err != nil {
		return false
	}

	result, err := mcp.RunTool(ctx, cfg, mcp.ExaServerName, exaFetchToolName, string(payload))
	if err != nil {
		fmt.Fprintf(out, "[Exa fetch failed: %s]\n\n", err)
		return false
	}
	if strings.TrimSpace(result.Content) == "" {
		return false
	}

	fmt.Fprintf(out, "%s\n\n", result.Content)
	return true
}

// writeLocalFetch reads one URL directly, spilling large pages to a file so
// the sub-agent can grep them.
func writeLocalFetch(ctx context.Context, out *strings.Builder, workingDir string, client *http.Client, rawURL string) {
	content, err := FetchURLAndConvert(ctx, client, rawURL)
	if err != nil {
		fmt.Fprintf(out, "## %s\n\n[fetch failed: %s]\n\n", rawURL, err)
		return
	}

	if len(content) <= LargeContentThreshold {
		fmt.Fprintf(out, "## %s\n\n%s\n\n", rawURL, content)
		return
	}

	tempFile, err := os.CreateTemp(workingDir, "page-*.md")
	if err != nil {
		fmt.Fprintf(out, "## %s\n\n[failed to save large page: %s]\n\n", rawURL, err)
		return
	}
	tempFilePath := tempFile.Name()

	if _, err := tempFile.WriteString(content); err != nil {
		_ = tempFile.Close()
		fmt.Fprintf(out, "## %s\n\n[failed to save large page: %s]\n\n", rawURL, err)
		return
	}
	if err := tempFile.Close(); err != nil {
		fmt.Fprintf(out, "## %s\n\n[failed to save large page: %s]\n\n", rawURL, err)
		return
	}

	fmt.Fprintf(out, "## %s\n\nFetched directly (large page). Content saved to: %s\n\n", rawURL, tempFilePath)
	fmt.Fprintf(out, "Use the view and grep tools to read it.\n\n")
}
