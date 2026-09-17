package tools

import (
	"encoding/json"
	"testing"

	"github.com/stretchr/testify/require"
)

func TestExaSearchParamsToWebSearch(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		input string
		want  map[string]any
	}{
		{
			name:  "num results maps to max results",
			input: `{"query":"go","numResults":5}`,
			want:  map[string]any{"query": "go", "max_results": float64(5)},
		},
		{
			name:  "filters without a fallback equivalent are dropped",
			input: `{"query":"go","numResults":3,"includeDomains":["example.com"],"type":"neural"}`,
			want:  map[string]any{"query": "go", "max_results": float64(3)},
		},
		{
			name:  "missing query is passed through for the fallback to reject",
			input: `{"numResults":5}`,
			want:  map[string]any{"numResults": float64(5)},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()

			out := exaSearchParamsToWebSearch(tt.input)
			var got map[string]any
			require.NoError(t, json.Unmarshal([]byte(out), &got))
			require.Equal(t, tt.want, got)
		})
	}

	t.Run("malformed input is passed through", func(t *testing.T) {
		t.Parallel()

		require.Equal(t, "not-json", exaSearchParamsToWebSearch("not-json"))
	})
}
