package mcp

import (
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"slices"
	"sync"
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

	cfg, ok := builtinStaticConfig(ExaServerName)
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

// TestBuiltinDisabled verifies the disable_exa switch keeps research off
// the built-in backend entirely, both when listing tools and when calling
// them, so callers fall back to DuckDuckGo and local fetching.
func TestBuiltinDisabled(t *testing.T) {
	t.Setenv(exaAPIKeyEnv, "")

	cfg := config.NewTestStore(&config.Config{
		Options: &config.Options{DisableExa: true, ExaAPIKey: "cfg-key"},
	})

	_, err := BuiltinTools(t.Context(), cfg, ExaServerName)
	require.ErrorContains(t, err, "disabled")

	_, err = RunTool(t.Context(), cfg, ExaServerName, "web_search_exa", `{}`)
	require.ErrorContains(t, err, "disabled")
}

// TestBuiltinDisabledClosesSession verifies turning the backend off also
// drops a session that was already connected, rather than leaving it open
// with nothing able to reach or clean it up.
func TestBuiltinDisabledClosesSession(t *testing.T) {
	t.Setenv(exaAPIKeyEnv, "")

	server := newBuiltinStub(t, nil)
	defer server.Close()

	restore := pointBuiltinAt(t, server.URL)
	defer restore()

	cfg := config.NewTestStore(&config.Config{})
	_, err := BuiltinTools(t.Context(), cfg, ExaServerName)
	require.NoError(t, err)

	stub := builtinServers[ExaServerName]
	stub.mu.Lock()
	require.NotNil(t, stub.session)
	stub.mu.Unlock()

	disabled := config.NewTestStore(&config.Config{
		Options: &config.Options{DisableExa: true},
	})
	_, err = BuiltinTools(t.Context(), disabled, ExaServerName)
	require.ErrorContains(t, err, "disabled")

	stub.mu.Lock()
	require.Nil(t, stub.session)
	stub.mu.Unlock()
}

// TestBuiltinToolsConnects wires the built-in backend to a stub MCP
// server and asserts both that it works and that nothing about it leaks
// into the shared registries an agent, the sidebar or a system prompt
// reads from.
func TestBuiltinToolsConnects(t *testing.T) {
	t.Setenv(exaAPIKeyEnv, "")

	server := newBuiltinStub(t, nil)
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

// TestBuiltinExaUserKey verifies that a configured key authenticates the
// built-in backend and switches off the pacing measured for the shared
// keyless quota.
func TestBuiltinExaUserKey(t *testing.T) {
	tests := []struct {
		name       string
		envKey     string
		optionKey  string
		wantKey    string
		wantPacing bool
	}{
		{
			name:      "config wins over environment",
			envKey:    "env-key",
			optionKey: "cfg-key",
			wantKey:   "cfg-key",
		},
		{
			name:    "environment fallback",
			envKey:  "env-key",
			wantKey: "env-key",
		},
		{
			name:       "keyless keeps pacing",
			wantPacing: true,
		},
		{
			name:      "unset expansion falls back to the environment",
			envKey:    "env-key",
			optionKey: "$CRUSH_TEST_UNSET_EXA_KEY",
			wantKey:   "env-key",
		},
		{
			name:       "unset expansion without an environment key stays keyless",
			optionKey:  "$CRUSH_TEST_UNSET_EXA_KEY",
			wantPacing: true,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			t.Setenv(exaAPIKeyEnv, tt.envKey)
			t.Setenv("CRUSH_TEST_UNSET_EXA_KEY", "")

			var log builtinKeyLog
			server := newBuiltinStub(t, log.observe)
			defer server.Close()

			restore := pointBuiltinAt(t, server.URL)
			defer restore()

			store := config.NewTestStore(&config.Config{
				Options: &config.Options{ExaAPIKey: tt.optionKey},
			})
			tools, err := BuiltinTools(t.Context(), store, ExaServerName)
			require.NoError(t, err)
			require.Len(t, tools, 1)

			require.Equal(t, []string{tt.wantKey}, log.values())

			m, ok := builtinEffectiveConfig(ExaServerName)
			require.True(t, ok)
			if tt.wantPacing {
				require.NotNil(t, m.RateLimit)
				require.NotNil(t, limiterForServer(store, ExaServerName))
			} else {
				require.Nil(t, m.RateLimit)
				require.Nil(t, limiterForServer(store, ExaServerName))
			}
		})
	}
}

// TestBuiltinExaKeyResolutionError verifies a configured key that cannot be
// expanded is reported, not silently downgraded to keyless traffic.
func TestBuiltinExaKeyResolutionError(t *testing.T) {
	t.Setenv(exaAPIKeyEnv, "")

	store := config.NewTestStore(&config.Config{
		Options: &config.Options{ExaAPIKey: "$(false)"},
	})

	_, err := BuiltinTools(t.Context(), store, ExaServerName)
	require.ErrorContains(t, err, "exa_api_key")
}

// TestBuiltinExaReconnectOnKeyChange verifies a live session is reused
// while the credentials hold and rebuilt when they change, so a newly
// configured key is picked up without restarting Crush.
func TestBuiltinExaReconnectOnKeyChange(t *testing.T) {
	t.Setenv(exaAPIKeyEnv, "")

	var log builtinKeyLog
	server := newBuiltinStub(t, log.observe)
	defer server.Close()

	restore := pointBuiltinAt(t, server.URL)
	defer restore()

	storeA := config.NewTestStore(&config.Config{
		Options: &config.Options{ExaAPIKey: "key-a"},
	})
	_, err := BuiltinTools(t.Context(), storeA, ExaServerName)
	require.NoError(t, err)

	// A second call with the same credentials pings and reuses the
	// session instead of reconnecting.
	_, err = BuiltinTools(t.Context(), storeA, ExaServerName)
	require.NoError(t, err)

	// Changing the key must rebuild the session rather than keep serving
	// from one bound to the old key.
	storeB := config.NewTestStore(&config.Config{
		Options: &config.Options{ExaAPIKey: "key-b"},
	})
	_, err = BuiltinTools(t.Context(), storeB, ExaServerName)
	require.NoError(t, err)

	require.Equal(t, []string{"key-a", "key-b"}, log.values())
}

// builtinKeyLog records the x-api-key header of every initialize request,
// so tests can assert both which key was used and whether a session was
// rebuilt.
type builtinKeyLog struct {
	mu   sync.Mutex
	keys []string
}

func (l *builtinKeyLog) observe(r *http.Request, method string) {
	if method != "initialize" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	l.keys = append(l.keys, r.Header.Get(exaAPIKeyHeader))
}

func (l *builtinKeyLog) values() []string {
	l.mu.Lock()
	defer l.mu.Unlock()
	return slices.Clone(l.keys)
}

// newBuiltinStub returns an MCP stub that accepts a connection and exposes
// a single web_search_exa tool. onRequest, when non-nil, sees every parsed
// request before it is handled.
func newBuiltinStub(t *testing.T, onRequest func(*http.Request, string)) *httptest.Server {
	t.Helper()

	return httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
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

		if onRequest != nil {
			onRequest(r, req.Method)
		}

		switch req.Method {
		case "initialize":
			w.Header().Set("Mcp-Session-Id", "stub-session")
			writeRPC(w, req.ID, map[string]any{
				"protocolVersion": "2025-06-18",
				"capabilities":    map[string]any{"tools": map[string]any{}},
				"serverInfo":      map[string]any{"name": "stub", "version": "0"},
			})
		case "ping":
			writeRPC(w, req.ID, map[string]any{})
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
}

// pointBuiltinAt redirects the built-in Exa server at a stub for one test and
// restores it afterwards, closing any session the test opened so the SDK's
// background goroutines do not outlive the stub server.
func pointBuiltinAt(t *testing.T, url string) func() {
	t.Helper()

	original := builtinServers[ExaServerName]

	// Build a fresh server rather than copying the original: builtinServer
	// carries a mutex, so copying it is a lock-copy bug.
	stubCfg, ok := builtinStaticConfig(ExaServerName)
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
