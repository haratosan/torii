package mcp

import (
	"context"
	"fmt"
	"strings"

	mcpclient "github.com/mark3labs/mcp-go/client"
	"github.com/mark3labs/mcp-go/client/transport"
	"github.com/mark3labs/mcp-go/mcp"
)

// MCPClient manages a single MCP server connection.
type MCPClient struct {
	name      string
	client    *mcpclient.Client
	tools     []mcp.Tool
	available bool
}

// NewStdioClient creates an MCP client that communicates via stdin/stdout with a subprocess.
func NewStdioClient(name string, command string, args []string) (*MCPClient, error) {
	c, err := mcpclient.NewStdioMCPClient(command, nil, args...)
	if err != nil {
		return nil, fmt.Errorf("stdio client %s: %w", name, err)
	}
	return &MCPClient{name: name, client: c}, nil
}

// NewSSEClient creates an MCP client that connects via HTTP SSE.
// Deprecated by the MCP spec in favour of Streamable HTTP; kept for older servers.
func NewSSEClient(name string, url string, headers map[string]string) (*MCPClient, error) {
	var opts []transport.ClientOption
	if len(headers) > 0 {
		opts = append(opts, transport.WithHeaders(headers))
	}
	c, err := mcpclient.NewSSEMCPClient(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("sse client %s: %w", name, err)
	}
	return &MCPClient{name: name, client: c}, nil
}

// NewStreamableHTTPClient creates an MCP client that connects via Streamable HTTP,
// the transport that supersedes SSE in the MCP spec.
func NewStreamableHTTPClient(name string, url string, headers map[string]string) (*MCPClient, error) {
	var opts []transport.StreamableHTTPCOption
	if len(headers) > 0 {
		opts = append(opts, transport.WithHTTPHeaders(headers))
	}
	c, err := mcpclient.NewStreamableHttpClient(url, opts...)
	if err != nil {
		return nil, fmt.Errorf("streamable http client %s: %w", name, err)
	}
	return &MCPClient{name: name, client: c}, nil
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

// Initialize performs the MCP protocol handshake and discovers tools.
func (m *MCPClient) Initialize(ctx context.Context) error {
	initReq := mcp.InitializeRequest{}
	initReq.Params.ProtocolVersion = mcp.LATEST_PROTOCOL_VERSION
	initReq.Params.ClientInfo = mcp.Implementation{
		Name:    "torii",
		Version: "1.0.0",
	}

	_, err := m.client.Initialize(ctx, initReq)
	if err != nil {
		return fmt.Errorf("initialize %s: %w", m.name, err)
	}

	m.available = true
	return nil
}

// ListTools discovers available tools from the MCP server.
func (m *MCPClient) ListTools(ctx context.Context) ([]mcp.Tool, error) {
	result, err := m.client.ListTools(ctx, mcp.ListToolsRequest{})
	if err != nil {
		return nil, fmt.Errorf("list tools %s: %w", m.name, err)
	}
	m.tools = result.Tools
	return m.tools, nil
}

// CallTool executes a tool on the MCP server.
func (m *MCPClient) CallTool(ctx context.Context, name string, arguments map[string]any) (string, error) {
	req := mcp.CallToolRequest{}
	req.Params.Name = name
	req.Params.Arguments = arguments

	result, err := m.client.CallTool(ctx, req)
	if err != nil {
		return "", fmt.Errorf("call tool %s/%s: %w", m.name, name, err)
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
	return m.available
}
