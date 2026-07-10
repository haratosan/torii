package mcp

import (
	"context"
	"io"
	"log/slog"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	mcpgo "github.com/mark3labs/mcp-go/mcp"
	mcpserver "github.com/mark3labs/mcp-go/server"
)

func testMCPServer() *mcpserver.MCPServer {
	s := mcpserver.NewMCPServer("verify", "1.0.0")
	s.AddTool(
		mcpgo.NewTool("ping", mcpgo.WithDescription("returns pong")),
		func(ctx context.Context, r mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
			return mcpgo.NewToolResultText("pong"), nil
		},
	)
	return s
}

func quietManager() *Manager {
	return NewManager(slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// Streamable HTTP end-to-end: handshake, tool discovery, call, and ${ENV} header expansion.
func TestStreamableHTTPEndToEnd(t *testing.T) {
	var gotAuth atomic.Value
	gotAuth.Store("")

	stream := mcpserver.NewStreamableHTTPServer(testMCPServer())
	ts := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		if a := r.Header.Get("Authorization"); a != "" {
			gotAuth.Store(a)
		}
		stream.ServeHTTP(w, r)
	}))
	defer ts.Close()

	t.Setenv("TORII_VERIFY_TOKEN", "s3cr3t")

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	m := quietManager()
	m.Start(ctx, []ServerConfig{{
		Name:      "verify",
		Transport: "http",
		URL:       ts.URL,
		Headers:   map[string]string{"Authorization": "Bearer ${TORII_VERIFY_TOKEN}"},
	}})
	defer m.Shutdown()

	if !m.HasTool("verify__ping") {
		t.Fatal("tool 'verify__ping' was not discovered over streamable http")
	}
	if got := len(m.Tools()); got != 1 {
		t.Fatalf("Tools() = %d defs, want 1", got)
	}

	resp, err := m.Execute(ctx, "verify__ping", `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("Execute returned tool error: %s", resp.Error)
	}
	if resp.Output != "pong" {
		t.Fatalf("Output = %q, want \"pong\"", resp.Output)
	}

	if got := gotAuth.Load().(string); got != "Bearer s3cr3t" {
		t.Fatalf("Authorization header = %q, want \"Bearer s3cr3t\" (env expansion broken)", got)
	}
}

// SSE regression guard. Two ways this has broken before: Start was never called (the
// client then hung in SendRequest waiting for the endpoint event), and Start was called
// with the handshake's timeout context (the event stream died with it, and the first
// tool call came back "Invalid session ID"). Both only surface on a real tool call.
func TestSSEEndToEnd(t *testing.T) {
	ts := mcpserver.NewTestServer(testMCPServer())
	defer ts.Close()

	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
	defer cancel()

	m := quietManager()
	m.Start(ctx, []ServerConfig{{Name: "sse", Transport: "sse", URL: ts.URL + "/sse"}})
	defer m.Shutdown()

	if !m.HasTool("sse__ping") {
		t.Fatal("tool 'sse__ping' was not discovered over sse")
	}
	resp, err := m.Execute(ctx, "sse__ping", `{}`)
	if err != nil {
		t.Fatalf("Execute: %v", err)
	}
	if resp.Output != "pong" {
		t.Fatalf("Output = %q, want \"pong\"", resp.Output)
	}
}

// siteServer publishes the tool names every site publishes, but each tool answers
// with the site that served it.
func siteServer(site string) *httptest.Server {
	s := mcpserver.NewMCPServer(site, "1.0.0")
	for _, name := range []string{"bash", "read_mqtt", "write_mqtt"} {
		n := name
		s.AddTool(
			mcpgo.NewTool(n, mcpgo.WithDescription("does "+n)),
			func(ctx context.Context, r mcpgo.CallToolRequest) (*mcpgo.CallToolResult, error) {
				return mcpgo.NewToolResultText(site + ":" + n), nil
			},
		)
	}
	return httptest.NewServer(mcpserver.NewStreamableHTTPServer(s))
}

// Two sites publishing identical tool names must stay individually addressable.
// Before qualifying, toolMap was keyed on the bare name and the servers — which start
// concurrently — raced to overwrite each other, so write_mqtt hit an arbitrary site.
// Repeated because the failure it guards against was nondeterministic.
func TestTwoSitesRouteIndependently(t *testing.T) {
	lz := siteServer("lenzburg")
	defer lz.Close()
	hd := siteServer("hochdorf")
	defer hd.Close()

	for i := 0; i < 10; i++ {
		ctx, cancel := context.WithTimeout(context.Background(), 15*time.Second)
		m := quietManager()
		m.Start(ctx, []ServerConfig{
			{Name: "lenzburg", Transport: "http", URL: lz.URL},
			{Name: "hochdorf", Transport: "http", URL: hd.URL},
		})

		if got := len(m.Tools()); got != 6 {
			t.Fatalf("run %d: Tools() = %d, want 6 distinct defs", i, got)
		}
		names := map[string]bool{}
		for _, d := range m.Tools() {
			if names[d.Name] {
				t.Fatalf("run %d: duplicate tool name exposed to the LLM: %s", i, d.Name)
			}
			names[d.Name] = true
		}

		for _, site := range []string{"lenzburg", "hochdorf"} {
			resp, err := m.Execute(ctx, site+"__write_mqtt", `{}`)
			if err != nil {
				t.Fatalf("run %d: Execute(%s): %v", i, site, err)
			}
			if want := site + ":write_mqtt"; resp.Output != want {
				t.Fatalf("run %d: routed to wrong site: got %q, want %q", i, resp.Output, want)
			}
		}

		// The bare name must no longer resolve — it is ambiguous by construction.
		if m.HasTool("write_mqtt") {
			t.Fatalf("run %d: bare 'write_mqtt' still resolves", i)
		}

		m.Shutdown()
		cancel()
	}
}

// startStreamableOn serves a fresh streamable-HTTP MCP server on the given listener.
// A fresh server means a fresh session, exactly as a real server restart would.
func startStreamableOn(ln net.Listener) *httptest.Server {
	stream := mcpserver.NewStreamableHTTPServer(testMCPServer())
	srv := httptest.NewUnstartedServer(http.HandlerFunc(stream.ServeHTTP))
	srv.Listener.Close()
	srv.Listener = ln
	srv.Start()
	return srv
}

// A server that briefly goes offline (update, restart) must not permanently break the
// connection: while down a call fails clearly, and once it returns a call reconnects and
// succeeds — without reconfiguring or restarting torii.
func TestReconnectAfterServerRestart(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("listen: %v", err)
	}
	addr := ln.Addr().String()
	srv := startStreamableOn(ln)

	ctx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
	defer cancel()

	m := quietManager()
	m.Start(ctx, []ServerConfig{{Name: "verify", Transport: "http", URL: srv.URL}})
	defer m.Shutdown()

	// Baseline: the connection works.
	resp, err := m.Execute(ctx, "verify__ping", `{}`)
	if err != nil || resp.Output != "pong" {
		t.Fatalf("baseline Execute: err=%v output=%q", err, resp)
	}

	// Server goes offline. The call must fail clearly (not hang), and the client must be
	// flagged unavailable so a reconnect is attempted rather than the dead transport reused.
	srv.Close()
	resp, err = m.Execute(ctx, "verify__ping", `{}`)
	if err != nil {
		t.Fatalf("Execute against down server returned a hard error: %v", err)
	}
	if !strings.Contains(resp.Error, "unreachable") {
		t.Fatalf("down server: Error = %q, want it to mention 'unreachable'", resp.Error)
	}
	if m.servers["verify"].Available() {
		t.Fatal("down server: client still reports Available() == true")
	}

	// Server comes back on the same address. The next call must reconnect and succeed.
	ln2, err := net.Listen("tcp", addr)
	if err != nil {
		t.Fatalf("re-listen on %s: %v", addr, err)
	}
	srv2 := startStreamableOn(ln2)
	defer srv2.Close()

	resp, err = m.Execute(ctx, "verify__ping", `{}`)
	if err != nil {
		t.Fatalf("Execute after restart: %v", err)
	}
	if resp.Error != "" {
		t.Fatalf("Execute after restart returned error: %s", resp.Error)
	}
	if resp.Output != "pong" {
		t.Fatalf("after restart: Output = %q, want \"pong\" (reconnect failed)", resp.Output)
	}
}

// A server name that is not a legal tool-name fragment must not yield an unusable name.
func TestQualifySanitizes(t *testing.T) {
	if got := qualify("haus.lenzburg", "write mqtt"); got != "haus_lenzburg__write_mqtt" {
		t.Fatalf("qualify = %q", got)
	}
}

// An unset ${VAR} must not silently produce a bare "Bearer " header.
func TestExpandHeadersUnsetVarWarns(t *testing.T) {
	m := quietManager()
	out := m.expandHeaders("s", map[string]string{"Authorization": "Bearer ${TORII_DEFINITELY_UNSET}"})
	if out["Authorization"] != "Bearer " {
		t.Fatalf("got %q", out["Authorization"])
	}
	if m.expandHeaders("s", nil) != nil {
		t.Fatal("nil headers should expand to nil")
	}
}
