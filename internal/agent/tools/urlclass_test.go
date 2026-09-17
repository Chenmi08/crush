package tools

import (
	"testing"

	"github.com/stretchr/testify/require"
)

func TestIsInternalURL(t *testing.T) {
	t.Parallel()

	tests := []struct {
		url  string
		want bool
	}{
		{"http://localhost:3000/mcp", true},
		{"http://localhost", true},
		{"https://api.localhost/v1", true},
		{"http://build.internal/status", true},
		{"http://nas.local", true},
		{"http://printer.lan", true},
		{"http://127.0.0.1:8080", true},
		{"http://127.1.2.3", true},
		{"http://10.0.0.5/docs", true},
		{"http://192.168.1.10", true},
		{"http://172.16.4.4", true},
		{"http://[::1]:9000", true},
		{"http://0.0.0.0", true},
		{"http://169.254.1.1", true},
		{"https://example.com", false},
		{"https://mcp.exa.ai/mcp", false},
		{"https://8.8.8.8", false},
		{"https://172.32.0.1", false}, // Just outside the private range.
		{"not a url at all", false},
		{"", false},
	}

	for _, tt := range tests {
		t.Run(tt.url, func(t *testing.T) {
			t.Parallel()
			require.Equal(t, tt.want, IsInternalURL(tt.url))
		})
	}
}
