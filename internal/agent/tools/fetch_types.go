package tools

// WebFetchToolName is the name of the web_fetch tool.
const WebFetchToolName = "web_fetch"

// WebSearchToolName is the name of the web_search tool for sub-agents.
const WebSearchToolName = "web_search"

// LargeContentThreshold is the size threshold for saving content to a file.
const LargeContentThreshold = 50000 // 50KB

// AgentPermissionsParams defines the permission parameters for a delegated
// sub-agent. A sub-agent only asks for permission when it will reach the
// network, so these are always the research shape: an optional URL plus the
// prompt describing what to find.
type AgentPermissionsParams struct {
	URL    string `json:"url,omitempty"`
	Prompt string `json:"prompt"`
}

// WebFetchParams defines the parameters for the web_fetch tool.
type WebFetchParams struct {
	URLs []string `json:"urls" description:"URLs to read. Batch every URL you need into a single call: one call costs one Exa request no matter how many URLs it carries, while separate calls are rate limited."`
	// MaxCharacters bounds how much text is extracted per page when a page
	// is read through Exa.
	MaxCharacters int `json:"max_characters,omitempty" description:"Characters to extract per page when a page is read through Exa (default 20000). Raise it for long documents."`
}

// WebSearchParams defines the parameters for the web_search tool.
type WebSearchParams struct {
	Query      string `json:"query" description:"The search query to find information on the web"`
	MaxResults int    `json:"max_results,omitempty" description:"Maximum number of results to return (default: 10, max: 20)"`
}

// FetchParams defines the parameters for the simple fetch tool.
type FetchParams struct {
	URL     string `json:"url" description:"The URL to fetch content from"`
	Format  string `json:"format" description:"The format to return the content in (text, markdown, or html)"`
	Timeout int    `json:"timeout,omitempty" description:"Optional timeout in seconds (max 120)"`
}

// FetchPermissionsParams defines the permission parameters for the simple fetch tool.
type FetchPermissionsParams struct {
	URL     string `json:"url"`
	Format  string `json:"format"`
	Timeout int    `json:"timeout,omitempty"`
}
