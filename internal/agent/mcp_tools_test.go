package agent

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestMCPToolAllowed(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name     string
		allowAll bool
		allowed  map[string][]string
		server   string
		tool     string
		want     bool
	}{
		{
			name:   "zero value denies everything",
			server: "exa", tool: "web_search_exa",
			want: false,
		},
		{
			name:    "empty allowlist denies everything",
			allowed: map[string][]string{}, server: "exa", tool: "web_search_exa",
			want: false,
		},
		{
			name:    "unlisted server is denied",
			allowed: map[string][]string{"other": nil}, server: "exa", tool: "web_search_exa",
			want: false,
		},
		{
			name:    "nil tool list grants the whole server",
			allowed: map[string][]string{"exa": nil}, server: "exa", tool: "web_search_exa",
			want: true,
		},
		{
			name:    "empty tool list grants the whole server",
			allowed: map[string][]string{"exa": {}}, server: "exa", tool: "web_fetch_exa",
			want: true,
		},
		{
			name:    "listed tool is granted",
			allowed: map[string][]string{"exa": {"web_search_exa"}}, server: "exa", tool: "web_search_exa",
			want: true,
		},
		{
			name:    "unlisted tool on a listed server is denied",
			allowed: map[string][]string{"exa": {"web_search_exa"}}, server: "exa", tool: "web_fetch_exa",
			want: false,
		},
		{
			name:     "allow all grants every server",
			allowAll: true, server: "exa", tool: "anything",
			want: true,
		},
		{
			name:     "allow all ignores the allowlist",
			allowAll: true, allowed: map[string][]string{}, server: "exa", tool: "anything",
			want: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, mcpToolAllowed(tt.allowAll, tt.allowed, tt.server, tt.tool))
		})
	}
}
