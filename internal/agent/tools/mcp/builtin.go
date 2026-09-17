package mcp

import (
	"context"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"log/slog"
	"os"
	"strings"
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

	// exaEndpoint is Exa's MCP endpoint. Keyless access exists only over
	// MCP: the REST API answers 402 without a key. Speaking MCP is what
	// lets the search backend work with no user configuration at all; a
	// user-supplied key is layered on at connect time as a header (see
	// exaAPIKeyHeader).
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

	// exaAPIKeyHeader authenticates the built-in backend with a personal
	// Exa API key. Exa's MCP server takes the key in this header; keeping
	// it out of the URL also keeps it out of request logs.
	exaAPIKeyHeader = "x-api-key"

	// exaAPIKeyEnv is the environment variable Exa's own tooling reads. It
	// is honored when options.exa_api_key is unset, so an existing Exa
	// setup works without further configuration.
	exaAPIKeyEnv = "EXA_API_KEY"

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
	// credentials fingerprints the credential inputs of the live session.
	// A key added, changed, or removed while a session is up would
	// otherwise be masked by the session ping: the old session stays
	// healthy but is bound to the old quota.
	credentials string
	// keyed records whether the live session authenticates with a user
	// key. The limiter path reads it to keep the keyless pacing off.
	keyed bool
}

// ensure returns a live session and its tool list, connecting on first use
// and reconnecting when the session has gone stale or its credentials have
// changed.
func (b *builtinServer) ensure(ctx context.Context, cfg *config.ConfigStore) (*ClientSession, []*Tool, error) {
	b.mu.Lock()
	defer b.mu.Unlock()

	fingerprint := credentialFingerprint(cfg)

	if b.session != nil {
		if fingerprint != b.credentials {
			slog.Debug("Built-in server credentials changed; reconnecting", "name", b.name)
			_ = b.session.Close()
			b.session, b.tools = nil, nil
		} else if err := pingSession(ctx, b.session, mcpTimeout(b.cfg)); err == nil {
			return b.session, b.tools, nil
		} else {
			// A stale session is dropped rather than renewed in place: this
			// server is not in the reconcile machinery, so nothing else would
			// clean it up.
			_ = b.session.Close()
			b.session, b.tools = nil, nil
		}
	}

	// Resolve credentials only when a connection is actually needed: the
	// key may be a shell substitution, and ensure runs before every call.
	m, keyed, err := b.connectConfig(cfg)
	if err != nil {
		return nil, nil, err
	}

	session, tools, err := b.connect(ctx, cfg, m)
	if err != nil {
		return nil, nil, err
	}
	b.session, b.tools = session, tools
	b.credentials, b.keyed = fingerprint, keyed
	return session, tools, nil
}

func (b *builtinServer) connect(ctx context.Context, cfg *config.ConfigStore, m config.MCPConfig) (*ClientSession, []*Tool, error) {
	mcpCtx, cancel := context.WithCancel(ctx)
	timer := time.AfterFunc(mcpTimeout(m), cancel)

	transport, _, err := createTransport(mcpCtx, cfg, b.name, m, cfg.Resolver())
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

// close drops the live session, if any, so a disabled backend stops
// holding a connection open.
func (b *builtinServer) close() {
	b.mu.Lock()
	defer b.mu.Unlock()

	if b.session != nil {
		_ = b.session.Close()
		b.session, b.tools = nil, nil
	}
}

// connectConfig returns the configuration to connect with, and whether a
// user key is in use. An explicitly configured key wins over the standard
// environment variable; whichever is used draws on the user's own Exa
// plan, so the pacing measured for the shared keyless quota is dropped.
// Throttling is still retried by callTool.
func (b *builtinServer) connectConfig(cfg *config.ConfigStore) (config.MCPConfig, bool, error) {
	m := b.cfg
	if b.name != ExaServerName {
		return m, false, nil
	}

	key, err := resolveExaKey(cfg)
	if err != nil {
		return m, false, err
	}
	if key == "" {
		return m, false, nil
	}

	m.Headers = map[string]string{exaAPIKeyHeader: key}
	m.RateLimit, m.RateBurst = nil, 0
	return m, true, nil
}

// resolveExaKey returns the key to authenticate with, or "" for the
// keyless backend. The configured option is expanded through the resolver;
// when it is unset, or expands to nothing, EXA_API_KEY is used instead.
func resolveExaKey(cfg *config.ConfigStore) (string, error) {
	raw := configuredExaKey(cfg)
	if raw != "" {
		resolved, err := cfg.Resolver().ResolveValue(raw)
		if err != nil {
			return "", fmt.Errorf("options.exa_api_key: %w", err)
		}
		if key := strings.TrimSpace(resolved); key != "" {
			return key, nil
		}
	}
	return strings.TrimSpace(os.Getenv(exaAPIKeyEnv)), nil
}

// configuredExaKey returns the raw options.exa_api_key value, or "" when
// unset. It must stay cheap: ensure runs before every built-in tool call.
func configuredExaKey(cfg *config.ConfigStore) string {
	if cfg == nil {
		return ""
	}
	c := cfg.Config()
	if c == nil || c.Options == nil {
		return ""
	}
	return strings.TrimSpace(c.Options.ExaAPIKey)
}

// credentialFingerprint fingerprints the credential inputs a session was
// built with. ensure runs before every built-in tool call, so this reads
// raw config and environment only: resolving a key may run arbitrary shell
// commands, which must not happen on the per-call path. A secret rotated
// behind $(cmd) is therefore picked up when the session is next rebuilt,
// not mid-flight.
func credentialFingerprint(cfg *config.ConfigStore) string {
	raw := configuredExaKey(cfg) + "\x00" + strings.TrimSpace(os.Getenv(exaAPIKeyEnv))
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}

// builtinEffectiveConfig returns a built-in server's effective
// configuration. Pacing is read from the state recorded at connect, so hot
// paths such as the per-call limiter lookup never expand values.
func builtinEffectiveConfig(name string) (config.MCPConfig, bool) {
	b, ok := builtinServers[name]
	if !ok {
		return config.MCPConfig{}, false
	}

	m := b.cfg
	b.mu.Lock()
	keyed := b.keyed
	b.mu.Unlock()
	if keyed {
		m.RateLimit, m.RateBurst = nil, 0
	}
	return m, true
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

// builtinStaticConfig returns the shipped configuration of a built-in
// server, without runtime layering.
func builtinStaticConfig(name string) (config.MCPConfig, bool) {
	b, ok := builtinServers[name]
	if !ok {
		return config.MCPConfig{}, false
	}
	return b.cfg, true
}

// builtinDisabled reports whether the user turned a built-in server off.
// Exa is the only switch today: options.disable_exa makes research use
// DuckDuckGo and local fetching instead.
func builtinDisabled(cfg *config.ConfigStore, name string) bool {
	if name != ExaServerName || cfg == nil {
		return false
	}
	c := cfg.Config()
	return c != nil && c.Options != nil && c.Options.DisableExa
}

// errBuiltinDisabled reports a built-in server the user turned off.
func errBuiltinDisabled(name string) error {
	return fmt.Errorf("built-in server %q is disabled", name)
}

// BuiltinTools returns the tools a built-in server exposes, connecting on
// first use. An error means the backend is unreachable right now, which
// callers treat as a signal to fall back.
func BuiltinTools(ctx context.Context, cfg *config.ConfigStore, name string) ([]*Tool, error) {
	b, ok := builtinServers[name]
	if !ok {
		return nil, fmt.Errorf("unknown built-in server %q", name)
	}
	if builtinDisabled(cfg, name) {
		b.close()
		return nil, errBuiltinDisabled(name)
	}

	_, tools, err := b.ensure(ctx, cfg)
	if err != nil {
		return nil, err
	}
	return tools, nil
}

func floatPtr(v float64) *float64 { return &v }
