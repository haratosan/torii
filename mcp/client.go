package mcp

import (
	"context"
	"fmt"
	"strings"
	"sync"
	"time"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// transportError marks a failure of the underlying connection (dead socket, invalid
// session after a server restart) as opposed to a tool-level error the server itself
// reported. Only the former is worth reconnecting for.
type transportError struct{ err error }

func (e *transportError) Error() string { return e.err.Error() }
func (e *transportError) Unwrap() error { return e.err }

// MCPClient manages a single MCP server connection.
type MCPClient struct {
	name string
	// newClient re-dials a fresh underlying connection. The old transport cannot be
	// reused once it dies, so reconnecting means building a new client from scratch.
	newClient func() (*mcpclient.Client, error)

	mu        sync.Mutex // guards client, tools, available
	client    *mcpclient.Client
	tools     []mcp.Tool
	available bool
}

// NewStdioClient creates an MCP client that communicates via stdin/stdout with a subprocess.
func NewStdioClient(name string, command string, args []string) (*MCPClient, error) {
	factory := func() (*mcpclient.Client, error) {
		return mcpclient.NewStdioMCPClient(command, nil, args...)
	}
	c, err := factory()
	if err != nil {
		return nil, fmt.Errorf("stdio client %s: %w", name, err)
	}
	return &MCPClient{name: name, newClient: factory, client: c}, nil
}

// NewSSEClient creates an MCP client that connects via HTTP SSE.
// Deprecated by the MCP spec in favour of Streamable HTTP; kept for older servers.
func NewSSEClient(name string, url string, headers map[string]string) (*MCPClient, error) {
	factory := func() (*mcpclient.Client, error) {
		var opts []transport.ClientOption
		if len(headers) > 0 {
			opts = append(opts, transport.WithHeaders(headers))
		}
		return mcpclient.NewSSEMCPClient(url, opts...)
	}
	c, err := factory()
	if err != nil {
		return nil, fmt.Errorf("sse client %s: %w", name, err)
	}
	return &MCPClient{name: name, newClient: factory, client: c}, nil
}

// NewStreamableHTTPClient creates an MCP client that connects via Streamable HTTP,
// the transport that supersedes SSE in the MCP spec.
func NewStreamableHTTPClient(name string, url string, headers map[string]string) (*MCPClient, error) {
	factory := func() (*mcpclient.Client, error) {
		var opts []transport.StreamableHTTPCOption
		if len(headers) > 0 {
			opts = append(opts, transport.WithHTTPHeaders(headers))
		}
		return mcpclient.NewStreamableHttpClient(url, opts...)
	}
	c, err := factory()
	if err != nil {
		return nil, fmt.Errorf("streamable http client %s: %w", name, err)
	}
	return &MCPClient{name: name, newClient: factory, client: c}, nil
}

// Start brings up the underlying transport. It must be called before Initialize:
// the HTTP transports do not connect on construction, and SSE blocks in SendRequest
// until Start has received the endpoint event.
//
// ctx must outlive the connection, not just the handshake — SSE ties its long-lived
// event stream to it, and cancelling ctx drops the session. Start is idempotent, and
// the stdio transport is already started by its constructor, so this is safe for all
// three transports.
func (m *MCPClient) Start(ctx context.Context) error {
	if err := m.client.Start(ctx); err != nil {
		return fmt.Errorf("start transport %s: %w", m.name, err)
	}
	return nil
}

// Initialize performs the MCP protocol handshake. It must be called before ListTools.
func (m *MCPClient) Initialize(ctx context.Context) error {
	m.mu.Lock()
	c := m.client
	m.mu.Unlock()

	if err := doInitialize(ctx, c, m.name); err != nil {
		return err
	}

	m.mu.Lock()
	m.available = true
	m.mu.Unlock()
	return nil
}

// doInitialize runs the MCP initialize protocol on the given underlying client. It
// touches no MCPClient state, so it is safe to call with or without m.mu held.
func doInitialize(ctx context.Context, c *mcpclient.Client, name string) error {
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "torii",
		Version: "1.0.0",
	}
	if _, err := c.Initialize(ctx, initReq); err != nil {
		return fmt.Errorf("initialize %s: %w", name, err)
	}
	return nil
}

// reconnect tears down the dead transport and dials a fresh one, redoing the handshake
// and tool discovery. Safe to call concurrently: the guard skips the re-dial if another
// caller already restored the connection, so a burst of failed calls reconnects once and
// the rest simply retry. ctx must outlive the connection (see Start).
func (m *MCPClient) reconnect(ctx context.Context) error {
	m.mu.Lock()
	defer m.mu.Unlock()

	// Someone else already reconnected between our failed call and now — nothing to do.
	if m.available {
		return nil
	}

	if m.client != nil {
		m.client.Close()
	}
	c, err := m.newClient()
	if err != nil {
		return fmt.Errorf("redial %s: %w", m.name, err)
	}
	m.client = c

	if err := c.Start(ctx); err != nil {
		return fmt.Errorf("restart transport %s: %w", m.name, err)
	}
	initCtx, cancel := context.WithTimeout(ctx, 30*time.Second)
	defer cancel()
	if err := doInitialize(initCtx, c, m.name); err != nil {
		return err
	}
	result, err := c.ListTools(initCtx, mcp.ListToolsRequest{})
	if err != nil {
		return fmt.Errorf("list tools %s: %w", m.name, err)
	}
	m.tools = result.Tools
	m.available = true
	return nil
}

// ListTools discovers available tools from the MCP server.
func (m *MCPClient) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	m.mu.Lock()
	c := m.client
	m.mu.Unlock()

	result, err := c.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools %s: %w", m.name, err)
	}

	m.mu.Lock()
	m.tools = result.Tools
	tools := m.tools
	m.mu.Unlock()
	return tools, nil
}

// CallTool executes a tool on the MCP server. A failure of the underlying transport is
// returned as *transportError so the manager can reconnect and retry; a tool-level error
// reported by the server is a plain error and must not trigger a reconnect.
func (m *MCPClient) CallTool(ctx context.Context, name string, arguments map[string]any) (string, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments

	m.mu.Lock()
	c := m.client
	m.mu.Unlock()

	result, err := c.CallTool(ctx, req)
	if err != nil {
		// Transport is dead (server offline, session invalidated by a restart). Flag it
		// so Tools() prunes the server and the next call reconnects first.
		m.mu.Lock()
		m.available = false
		m.mu.Unlock()
		return "", &transportError{fmt.Errorf("call tool %s/%s: %w", m.name, name, err)}
	}

	if result.IsError {
		var parts []string
		for _, c := range result.Content {
			if tc, ok := c.(mcp.TextContent); ok {
				parts = append(parts, tc.Text)
			}
		}
		return "", fmt.Errorf("tool error: %s", strings.Join(parts, "\n"))
	}

	var parts []string
	for _, c := range result.Content {
		if tc, ok := c.(mcp.TextContent); ok {
			parts = append(parts, tc.Text)
		}
	}
	return strings.Join(parts, "\n"), nil
}

// Close shuts down the MCP client connection.
func (m *MCPClient) Close() error {
	if m.client != nil {
		return m.client.Close()
	}
	return nil
}

// Name returns the server name.
func (m *MCPClient) Name() string {
	return m.name
}

// Available returns whether the server is initialized and usable.
func (m *MCPClient) Available() bool {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.available
}

// snapshot returns availability and the current tool list atomically, so a concurrent
// reconnect swapping the tools slice cannot be observed half-updated.
func (m *MCPClient) snapshot() (bool, []mcp.Tool) {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.available, m.tools
}
