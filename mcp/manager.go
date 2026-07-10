package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/llm"
)

// ServerConfig describes one MCP server from config.yaml.
type ServerConfig struct {
	Name      string            `yaml:"name"`
	Transport string            `yaml:"transport"` // "stdio", "http" (streamable) or "sse"
	Command   string            `yaml:"command"`   // for stdio
	Args      []string          `yaml:"args"`      // for stdio
	URL       string            `yaml:"url"`       // for http and sse
	Headers   map[string]string `yaml:"headers"`   // for http and sse; values expand ${ENV_VAR}
}

// toolRef points a qualified tool name back at the server that serves it and the
// bare name that server knows it by.
type toolRef struct {
	server string
	tool   string
}

// Manager manages all MCP server connections.
type Manager struct {
	servers map[string]*MCPClient
	// toolMap maps qualified tool name ("<server>__<tool>") → the server and bare
	// name to call. Qualifying is what keeps two sites that publish identical tool
	// names (bash, write_mqtt) from silently overwriting each other.
	toolMap map[string]toolRef
	mu      sync.RWMutex
	logger  *slog.Logger
}

// NewManager creates a new MCP manager.
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		servers: make(map[string]*MCPClient),
		toolMap: make(map[string]toolRef),
		logger:  logger,
	}
}

// sanitizeNamePart strips characters that LLM tool-name schemas reject, so a server
// named "haus.lenzburg" cannot produce an unusable tool name.
func sanitizeNamePart(s string) string {
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= 'A' && r <= 'Z', r >= '0' && r <= '9', r == '_', r == '-':
			b.WriteRune(r)
		default:
			b.WriteRune('_')
		}
	}
	return b.String()
}

// qualify builds the tool name the LLM sees.
func qualify(server, tool string) string {
	return sanitizeNamePart(server) + "__" + sanitizeNamePart(tool)
}

// Start initializes all configured MCP servers concurrently.
func (m *Manager) Start(ctx context.Context, configs []ServerConfig) {
	var wg sync.WaitGroup
	for _, cfg := range configs {
		wg.Add(1)
		go func(cfg ServerConfig) {
			defer wg.Done()
			m.startServer(ctx, cfg)
		}(cfg)
	}
	wg.Wait()
}

func (m *Manager) startServer(ctx context.Context, cfg ServerConfig) {
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()

	var client *MCPClient
	var err error

	switch cfg.Transport {
	case "stdio":
		client, err = NewStdioClient(cfg.Name, cfg.Command, cfg.Args)
	case "http", "streamable-http":
		client, err = NewStreamableHTTPClient(cfg.Name, cfg.URL, m.expandHeaders(cfg.Name, cfg.Headers))
	case "sse":
		client, err = NewSSEClient(cfg.Name, cfg.URL, m.expandHeaders(cfg.Name, cfg.Headers))
	default:
		m.logger.Error("unknown mcp transport", "name", cfg.Name, "transport", cfg.Transport)
		return
	}

	if err != nil {
		m.logger.Error("mcp client creation failed", "name", cfg.Name, "error", err)
		return
	}

	// Deliberately ctx, not initCtx: SSE binds its event stream to this context, so a
	// handshake-scoped one would tear the session down as soon as startServer returns.
	if err := client.Start(ctx); err != nil {
		m.logger.Error("mcp transport start failed", "name", cfg.Name, "error", err)
		client.Close()
		return
	}

	if err := client.Initialize(initCtx); err != nil {
		m.logger.Error("mcp initialize failed", "name", cfg.Name, "error", err)
		client.Close()
		return
	}

	tools, err := client.ListTools(initCtx)
	if err != nil {
		m.logger.Error("mcp list tools failed", "name", cfg.Name, "error", err)
		client.Close()
		return
	}

	m.mu.Lock()
	if _, dup := m.servers[cfg.Name]; dup {
		m.mu.Unlock()
		m.logger.Error("duplicate mcp server name, ignoring second one", "name", cfg.Name)
		client.Close()
		return
	}
	m.servers[cfg.Name] = client
	for _, t := range tools {
		qn := qualify(cfg.Name, t.Name)
		// Distinct server names can still sanitize to the same prefix ("a.b" and "a_b"),
		// which would silently reintroduce the overwrite that qualifying exists to prevent.
		if prev, clash := m.toolMap[qn]; clash {
			m.logger.Error("qualified mcp tool name collision, keeping first",
				"exposed_as", qn, "kept", prev.server, "dropped", cfg.Name)
			continue
		}
		m.toolMap[qn] = toolRef{server: cfg.Name, tool: t.Name}
		m.logger.Info("mcp tool discovered", "server", cfg.Name, "tool", t.Name, "exposed_as", qn)
	}
	m.mu.Unlock()

	m.logger.Info("mcp server started", "name", cfg.Name, "tools", len(tools))
}

// expandHeaders resolves ${ENV_VAR} references in header values so that secrets
// live in the environment rather than in config.yaml. An unset variable expands to
// the empty string, which turns into a confusing 401 downstream — so warn loudly.
func (m *Manager) expandHeaders(server string, headers map[string]string) map[string]string {
	if len(headers) == 0 {
		return nil
	}
	out := make(map[string]string, len(headers))
	for k, v := range headers {
		out[k] = os.Expand(v, func(name string) string {
			val := os.Getenv(name)
			if val == "" {
				m.logger.Warn("mcp header env var is unset or empty",
					"server", server, "header", k, "var", name)
			}
			return val
		})
	}
	return out
}

// Tools returns all MCP tools as llm.ToolDef for the agent.
func (m *Manager) Tools() []llm.ToolDef {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var defs []llm.ToolDef
	for serverName, client := range m.servers {
		if !client.Available() {
			continue
		}
		for _, t := range client.tools {
			params := map[string]any{
				"type":       t.InputSchema.Type,
				"properties": t.InputSchema.Properties,
			}
			if len(t.InputSchema.Required) > 0 {
				required := make([]any, len(t.InputSchema.Required))
				for i, r := range t.InputSchema.Required {
					required[i] = r
				}
				params["required"] = required
			}
			// The server name is the only thing distinguishing two sites that publish
			// identical tools, so it goes in the description too — the model routes on
			// prose, not just on the name.
			defs = append(defs, llm.ToolDef{
				Name:        qualify(serverName, t.Name),
				Description: "[" + serverName + "] " + t.Description,
				Parameters:  params,
			})
		}
	}
	return defs
}

// Execute calls an MCP tool and returns an ExtResponse.
func (m *Manager) Execute(ctx context.Context, toolName string, input string) (*extension.ExtResponse, error) {
	m.mu.RLock()
	ref, ok := m.toolMap[toolName]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("unknown mcp tool: %s", toolName)
	}
	client, ok := m.servers[ref.server]
	m.mu.RUnlock()

	if !ok || !client.Available() {
		return nil, fmt.Errorf("mcp server %s not available", ref.server)
	}

	var arguments map[string]any
	if input != "" {
		if err := json.Unmarshal([]byte(input), &arguments); err != nil {
			return nil, fmt.Errorf("parse mcp tool arguments: %w", err)
		}
	}

	// The server knows the tool by its bare name, not the qualified one.
	output, err := client.CallTool(ctx, ref.tool, arguments)
	if err != nil {
		return &extension.ExtResponse{Error: err.Error()}, nil
	}

	return &extension.ExtResponse{Output: output}, nil
}

// HasTool returns true if the given tool name is served by an MCP server.
func (m *Manager) HasTool(name string) bool {
	m.mu.RLock()
	defer m.mu.RUnlock()
	_, ok := m.toolMap[name]
	return ok
}

// Shutdown closes all MCP server connections.
func (m *Manager) Shutdown() {
	m.mu.Lock()
	defer m.mu.Unlock()
	for name, client := range m.servers {
		if err := client.Close(); err != nil {
			m.logger.Error("mcp client close error", "name", name, "error", err)
		}
	}
	m.servers = make(map[string]*MCPClient)
	m.toolMap = make(map[string]toolRef)
}
