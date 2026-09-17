package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/stretchr/testify/require"

	"github.com/charmbracelet/crush/internal/config"
)

func TestBuiltinRegistry(t *testing.T) {
	t.Parallel()

	require.True(t, IsBuiltin(ExaServerName))
	// The name is namespaced so a user's own "exa" server coexists rather
	// than being shadowed by the built-in one.
	require.False(t, IsBuiltin("exa"))
	require.False(t, IsBuiltin(""))

	cfg, ok := builtinConfig(ExaServerName)
	require.True(t, ok)
	require.Equal(t, config.MCPHttp, cfg.Type)
	require.NotEmpty(t, cfg.URL)

	require.NotNil(t, cfg.RateLimit, "the built-in backend must be paced by default")
	require.InDelta(t, 2.5, *cfg.RateLimit, 0.001)
	require.Equal(t, 3, cfg.RateBurst)

	// An explicit tools parameter replaces the server's defaults, so every
	// tool the routed fetch calls internally has to be named in the URL.
	// Dropping one only fails at runtime, with no compile-time signal.
	for _, tool := range []string{"web_search_exa", "web_search_advanced_exa", "web_fetch_exa"} {
		require.Contains(t, cfg.URL, tool)
	}
}

func TestBuiltinUnknownServer(t *testing.T) {
	t.Parallel()

	cfg := config.NewTestStore(&config.Config{})

	_, err := BuiltinTools(t.Context(), cfg, "not-a-builtin")
	require.ErrorContains(t, err, "unknown built-in server")
}

// TestBuiltinToolsConnects wires the built-in backend to a stub MCP
// server and asserts both that it works and that nothing about it leaks
// into the shared registries an agent, the sidebar or a system prompt
// reads from.
func TestBuiltinToolsConnects(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if r.Body == nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}
		body, _ := io.ReadAll(r.Body)

		var req struct {
			ID     json.RawMessage `json:"id"`
			Method string          `json:"method"`
		}
		if err := json.Unmarshal(body, &req); err != nil {
			w.WriteHeader(http.StatusAccepted)
			return
		}

		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "stub-session")
			writeRPC(w, req.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "stub", "version": "0"},
			})
		case "tools/list":
			writeRPC(w, req.ID, map[string]any{
				"tools": []any{map[string]any{
					"name":        "web_search_exa",
					"description": "stub search",
					"inputSchema": map[string]any{"type": "object"},
				}},
			})
		default:
			// Notifications carry no id and are answered with 202; any other
			// unmodelled method (the SEP-2575 server/discover probe, for
			// one) gets a proper JSON-RPC error so the SDK moves on.
			if len(req.ID) == 0 {
				w.WriteHeader(http.StatusAccepted)
				return
			}
			writeRPCError(w, req.ID, -32601, "method not found")
		}
	}))
	defer server.Close()

	restore := pointBuiltinAt(t, server.URL)
	defer restore()

	cfg := config.NewTestStore(&config.Config{})
	tools, err := BuiltinTools(t.Context(), cfg, ExaServerName)
	require.NoError(t, err)
	require.Len(t, tools, 1)
	require.Equal(t, "web_search_exa", tools[0].Name)

	_, exposed := GetState(ExaServerName)
	require.False(t, exposed, "a built-in server must not enter MCP state")

	for name := range Tools() {
		require.NotEqual(t, ExaServerName, name, "a built-in server must not enter the tool registry")
	}
}

// pointBuiltinAt redirects the built-in Exa server at a stub for one test and
// restores it afterwards, closing any session the test opened so the SDK's
// background goroutines do not outlive the stub server.
func pointBuiltinAt(t *testing.T, url string) func() {
	t.Helper()

	original := builtinServers[ExaServerName]

	// Build a fresh server rather than copying the original: builtinServer
	// carries a mutex, so copying it is a lock-copy bug.
	stubCfg, ok := builtinConfig(ExaServerName)
	require.True(t, ok)
	stubCfg.URL = url
	stub := &builtinServer{name: ExaServerName, cfg: stubCfg}
	builtinServers[ExaServerName] = stub

	return func() {
		stub.mu.Lock()
		if stub.session != nil {
			_ = stub.session.Close()
			stub.session, stub.tools = nil, nil
		}
		stub.mu.Unlock()

		builtinServers[ExaServerName] = original
	}
}

func writeRPC(w http.ResponseWriter, id json.RawMessage, result any) {
	if len(id) == 0 {
		id = json.RawMessage("1")
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"result":  result,
	})
}

func writeRPCError(w http.ResponseWriter, id json.RawMessage, code int, message string) {
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(map[string]any{
		"jsonrpc": "2.0",
		"id":      id,
		"error":   map[string]any{"code": code, "message": message},
	})
}
