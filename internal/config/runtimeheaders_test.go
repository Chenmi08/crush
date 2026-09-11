package config

import (
	"testing"

	"github.com/charmbracelet/crush/internal/env"
	"github.com/stretchr/testify/require"
)

func TestProtectRuntimeVars(t *testing.T) {
	t.Parallel()

	tests := []struct {
		name  string
		in    string
		out   string
		found bool
	}{
		{"bare", "$CRUSH_SESSION_ID", "{{CRUSH_SESSION_ID}}", true},
		{"braced", "${CRUSH_SESSION_ID}", "{{CRUSH_SESSION_ID}}", true},
		{"embedded", "Bearer ${CRUSH_SESSION_HASH}", "Bearer {{CRUSH_SESSION_HASH}}", true},
		{"dash suffix", "$CRUSH_SESSION_ID-suffix", "{{CRUSH_SESSION_ID}}-suffix", true},
		{"word suffix", "${CRUSH_SESSION_ID}suffix", "{{CRUSH_SESSION_ID}}suffix", true},
		{"longer name", "$CRUSH_SESSION_IDX", "$CRUSH_SESSION_IDX", false},
		{"env var", "$OPENAI_API_KEY", "$OPENAI_API_KEY", false},
	}
	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Parallel()
			out, found := protectRuntimeVars(tt.in)
			require.Equal(t, tt.out, out)
			require.Equal(t, tt.found, found)
		})
	}
}

func TestResolveProviderHeaders(t *testing.T) {
	t.Parallel()

	resolver := NewShellVariableResolver(env.NewFromMap(map[string]string{"FOO": "bar"}))
	static, runtimeHeaders, err := resolveProviderHeaders(map[string]string{
		"X-Static":  "$FOO",
		"X-Session": "${CRUSH_SESSION_ID}",
		"X-Mixed":   "Bearer $FOO/$CRUSH_SESSION_HASH",
		"X-Dropped": "$MISSING",
	}, resolver, "test")
	require.NoError(t, err)
	require.Equal(t, map[string]string{"X-Static": "bar"}, static)
	require.Equal(t, map[string]string{
		"X-Session": "{{CRUSH_SESSION_ID}}",
		"X-Mixed":   "Bearer bar/{{CRUSH_SESSION_HASH}}",
	}, runtimeHeaders)
}

func TestResolveProviderHeaders_Error(t *testing.T) {
	t.Parallel()

	resolver := NewShellVariableResolver(env.NewFromMap(nil))
	_, _, err := resolveProviderHeaders(map[string]string{
		"X-Fail": "$(false)",
	}, resolver, "test")
	require.Error(t, err)
	require.Contains(t, err.Error(), `resolving provider test header "X-Fail"`)
}

func TestExpandRuntimeHeaders(t *testing.T) {
	t.Parallel()

	headers := ExpandRuntimeHeaders(map[string]string{
		"X-Session": "{{CRUSH_SESSION_ID}}",
		"X-Both":    "{{CRUSH_SESSION_ID}}-{{CRUSH_SESSION_HASH}}",
		"X-Message": "{{CRUSH_MESSAGE_ID}}",
	}, map[string]string{
		RuntimeVarSessionID:   "sess",
		RuntimeVarSessionHash: "hash",
		RuntimeVarMessageID:   "",
	})
	require.Equal(t, map[string]string{
		"X-Session": "sess",
		"X-Both":    "sess-hash",
	}, headers)
}
