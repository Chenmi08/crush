package mcp

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/modelcontextprotocol/go-sdk/mcp"

	"github.com/charmbracelet/crush/internal/config"
	"github.com/charmbracelet/crush/internal/version"
)

// Built-in servers are MCP endpoints Crush ships and owns.
//
// They are deliberately absent from every shared registry: not in the
// session map the agent-visible tool list is built from, not in the MCP
// sidebar's state, and not in the MCP instructions appended to system
// prompts. Nothing an agent is handed can reach one — the only way in is to
// ask for it by name. That keeps a built-in backend out of the main
// conversation structurally, rather than filtering it out afterwards.
const (
	// ExaServerName identifies the built-in web search backend.
	ExaServerName = "builtin-exa"

	// exaEndpoint is Exa's keyless MCP endpoint. Keyless access exists only
	// over MCP: the REST API answers 402 without a key. Speaking MCP is what
	// lets the search backend work with no user configuration at all.
	//
	// An explicit tools list replaces the server's defaults, so every tool
	// we call has to be named here — including web_fetch_exa, which the
	// routed web_fetch tool calls internally.
	exaEndpoint = "https://mcp.exa.ai/mcp?tools=web_search_exa,web_fetch_exa,web_search_advanced_exa"

	// exaRateLimit and exaRateBurst come from measuring the keyless
	// endpoint: a burst of about three requests refilling at roughly three
	// per second, with search and fetch sharing one pool. Limiting just
	// below the measured ceiling keeps calls queued instead of rejected.
	exaRateLimit = 2.5
	exaRateBurst = 3

	// builtinTimeout bounds connecting to and pinging a built-in server.
	builtinTimeout = 30
)

// builtinServer owns a lazily connected session for one built-in endpoint.
// The session is private: it is never written to the shared registries.
type builtinServer struct {
	name string
	cfg  config.MCPConfig

	mu      sync.Mutex
	session *ClientSession
	tools   []*Tool
}

// ensure returns a live session and its tool list, connecting on first use
// and reconnecting when the session has gone stale.
func (b *builtinServer) ensure(ctx context.Context, cfg *config.ConfigStore) (*ClientSession, []*Tool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.session != nil {
		if err := pingSession(ctx, b.session, mcpTimeout(b.cfg)); err == nil {
			return b.session, b.tools, nil
		}
		// A stale session is dropped rather than renewed in place: this
		// server is not in the reconcile machinery, so nothing else would
		// clean it up.
		_ = b.session.Close()
		b.session, b.tools = nil, nil
	}

	session, tools, err := b.connect(ctx, cfg)
	if err != nil {
		return nil, nil, err
	}
	b.session, b.tools = session, tools
	return session, tools, nil
}

func (b *builtinServer) connect(ctx context.Context, cfg *config.ConfigStore) (*ClientSession, []*Tool, error) {
	mcpCtx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(mcpTimeout(b.cfg), cancel)

	transport, _, err := createTransport(mcpCtx, cfg, b.name, b.cfg, cfg.Resolver())
	if err != nil {
		timer.Stop()
		cancel()
		return nil, nil, fmt.Errorf("built-in %s transport: %w", b.name, err)
	}

	client := mcp.NewClient(
		&mcp.Implementation{
			Name:    "crush",
			Version: version.Version,
			Title:   "Crush",
		},
		nil,
	)

	raw, err := client.Connect(mcpCtx, transport, nil)
	timer.Stop()
	if err != nil {
		cancel()
		return nil, nil, fmt.Errorf("built-in %s connect: %w", b.name, err)
	}

	session := &ClientSession{ClientSession: raw, cancel: cancel}

	tools, err := getTools(ctx, session)
	if err != nil {
		_ = session.Close()
		return nil, nil, fmt.Errorf("built-in %s tools: %w", b.name, err)
	}
	if len(tools) == 0 {
		_ = session.Close()
		return nil, nil, fmt.Errorf("built-in %s exposed no tools", b.name)
	}

	return session, tools, nil
}

// builtinServers is the registry of endpoints Crush ships. The keys are
// namespaced so they cannot collide with a user's own MCP server.
var builtinServers = map[string]*builtinServer{
	ExaServerName: {
		name: ExaServerName,
		cfg: config.MCPConfig{
			Type:      config.MCPHttp,
			URL:       exaEndpoint,
			RateLimit: floatPtr(exaRateLimit),
			RateBurst: exaRateBurst,
			Timeout:   builtinTimeout,
		},
	},
}

// IsBuiltin reports whether name identifies a built-in server.
func IsBuiltin(name string) bool {
	_, ok := builtinServers[name]
	return ok
}

// builtinConfig returns the configuration of a built-in server.
func builtinConfig(name string) (config.MCPConfig, bool) {
	b, ok := builtinServers[name]
	if !ok {
		return config.MCPConfig{}, false
	}
	return b.cfg, true
}

// BuiltinTools returns the tools a built-in server exposes, connecting on
// first use. An error means the backend is unreachable right now, which
// callers treat as a signal to fall back.
func BuiltinTools(ctx context.Context, cfg *config.ConfigStore, name string) ([]*Tool, error) {
	b, ok := builtinServers[name]
	if !ok {
		return nil, fmt.Errorf("unknown built-in server %q", name)
	}

	_, tools, err := b.ensure(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return tools, nil
}

func floatPtr(v float64) *float64 { return &v }
