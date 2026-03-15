package mcp

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"sync"
	"time"

	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/llm"
)

// ServerConfig describes one MCP server from config.yaml.
type ServerConfig struct {
	Name      string   `yaml:"name"`
	Transport string   `yaml:"transport"` // "stdio" or "sse"
	Command   string   `yaml:"command"`   // for stdio
	Args      []string `yaml:"args"`      // for stdio
	URL       string   `yaml:"url"`       // for sse
}

// Manager manages all MCP server connections.
type Manager struct {
	servers map[string]*MCPClient
	// toolMap maps tool name → server name for routing
	toolMap map[string]string
	mu      sync.RWMutex
	logger  *slog.Logger
}

// NewManager creates a new MCP manager.
func NewManager(logger *slog.Logger) *Manager {
	return &Manager{
		servers: make(map[string]*MCPClient),
		toolMap: make(map[string]string),
		logger:  logger,
	}
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
	case "sse":
		client, err = NewSSEClient(cfg.Name, cfg.URL)
	default:
		m.logger.Error("unknown mcp transport", "name", cfg.Name, "transport", cfg.Transport)
		return
	}

	if err != nil {
		m.logger.Error("mcp client creation failed", "name", cfg.Name, "error", err)
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
	m.servers[cfg.Name] = client
	for _, t := range tools {
		m.toolMap[t.Name] = cfg.Name
		m.logger.Info("mcp tool discovered", "server", cfg.Name, "tool", t.Name)
	}
	m.mu.Unlock()

	m.logger.Info("mcp server started", "name", cfg.Name, "tools", len(tools))
}

// Tools returns all MCP tools as llm.ToolDef for the agent.
func (m *Manager) Tools() []llm.ToolDef {
	m.mu.RLock()
	defer m.mu.RUnlock()

	var defs []llm.ToolDef
	for _, client := range m.servers {
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
			defs = append(defs, llm.ToolDef{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  params,
			})
		}
	}
	return defs
}

// Execute calls an MCP tool and returns an ExtResponse.
func (m *Manager) Execute(ctx context.Context, toolName string, input string) (*extension.ExtResponse, error) {
	m.mu.RLock()
	serverName, ok := m.toolMap[toolName]
	if !ok {
		m.mu.RUnlock()
		return nil, fmt.Errorf("unknown mcp tool: %s", toolName)
	}
	client, ok := m.servers[serverName]
	m.mu.RUnlock()

	if !ok || !client.Available() {
		return nil, fmt.Errorf("mcp server %s not available", serverName)
	}

	var arguments map[string]any
	if input != "" {
		if err := json.Unmarshal([]byte(input), &arguments); err != nil {
			return nil, fmt.Errorf("parse mcp tool arguments: %w", err)
		}
	}

	output, err := client.CallTool(ctx, toolName, arguments)
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
	m.toolMap = make(map[string]string)
}
