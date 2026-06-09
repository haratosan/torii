package tui

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	tea "github.com/charmbracelet/bubbletea"
)

// chatMessage matches the wire format the OpenAI-compatible API expects on
// /v1/chat/completions. Keeping a local copy avoids importing the api
// package — the TUI is a regular HTTP client.
type chatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type chatRequest struct {
	Model    string        `json:"model"`
	Messages []chatMessage `json:"messages"`
	Stream   bool          `json:"stream"`
}

// Stream event messages — exposed to the Bubbletea program as tea.Msg
// payloads. Names are exported so view.go can type-switch on them.

type StreamTokenMsg struct{ Text string }
type StreamToolCallMsg struct {
	Name string
	Args string
}
type StreamToolResultMsg struct {
	Name   string
	Output string
	Err    string
}
type StreamDoneMsg struct{}
type StreamErrorMsg struct{ Err error }

// chunkChoice mirrors the relevant subset of the OpenAI streaming chunk
// schema. We only care about delta.content; role is only set on the first
// chunk and we ignore it.
type chunkChoice struct {
	Delta struct {
		Content string `json:"content"`
	} `json:"delta"`
	FinishReason *string `json:"finish_reason"`
}

type chunkFrame struct {
	Choices []chunkChoice `json:"choices"`

	// Vendor extension — present on tool_call / tool_result frames emitted
	// when the request was made with X-Torii-Events: verbose. OpenAI clients
	// ignore frames whose `choices` is empty; torii's TUI dispatches on this
	// discriminator instead.
	ToriiEvent string `json:"torii_event"`
	Name       string `json:"name"`
	Arguments  string `json:"arguments"`
	Output     string `json:"output"`
	Error      string `json:"error"`
	Message    string `json:"message"`
}

// Client is the thin HTTP wrapper the TUI uses to talk to the torii daemon.
type Client struct {
	BaseURL string
	Token   string
	Model   string

	http *http.Client
}

func newClient(cfg *tuiConfig) *Client {
	return &Client{
		BaseURL: strings.TrimRight(cfg.BaseURL, "/"),
		Token:   cfg.Token,
		Model:   cfg.Model,
		// No top-level timeout: streaming responses run as long as the agent
		// takes. Cancellation is via context from the caller.
		http: &http.Client{Timeout: 0},
	}
}

// Stream fires a chat completion with verbose events enabled and returns a
// channel of tea.Msg payloads. The channel is closed after StreamDoneMsg or
// StreamErrorMsg — readers should drain until close.
func (c *Client) Stream(ctx context.Context, history []chatMessage) <-chan tea.Msg {
	out := make(chan tea.Msg, 32)

	go func() {
		defer close(out)

		body, err := json.Marshal(chatRequest{
			Model:    c.Model,
			Messages: history,
			Stream:   true,
		})
		if err != nil {
			out <- StreamErrorMsg{Err: fmt.Errorf("marshal request: %w", err)}
			return
		}

		req, err := http.NewRequestWithContext(ctx, http.MethodPost,
			c.BaseURL+"/v1/chat/completions", bytes.NewReader(body))
		if err != nil {
			out <- StreamErrorMsg{Err: err}
			return
		}
		req.Header.Set("Authorization", "Bearer "+c.Token)
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Accept", "text/event-stream")
		req.Header.Set("X-Torii-Events", "verbose")

		resp, err := c.http.Do(req)
		if err != nil {
			out <- StreamErrorMsg{Err: fmt.Errorf("request: %w", err)}
			return
		}
		defer resp.Body.Close()

		if resp.StatusCode != http.StatusOK {
			b, _ := io.ReadAll(io.LimitReader(resp.Body, 4096))
			out <- StreamErrorMsg{Err: fmt.Errorf("http %d: %s", resp.StatusCode, strings.TrimSpace(string(b)))}
			return
		}

		parseSSE(ctx, resp.Body, out)
		out <- StreamDoneMsg{}
	}()

	return out
}

// parseSSE reads one SSE frame at a time from r and emits typed messages to
// out. Stops on context cancel, [DONE] sentinel, or EOF.
func parseSSE(ctx context.Context, r io.Reader, out chan<- tea.Msg) {
	sc := bufio.NewScanner(r)
	// Default Scanner buffer (64 KiB) is plenty for normal text deltas but
	// can be tripped by long tool_result payloads. Bump to 1 MiB.
	sc.Buffer(make([]byte, 0, 64*1024), 1<<20)

	for sc.Scan() {
		if ctx.Err() != nil {
			return
		}
		line := sc.Text()
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "" || payload == "[DONE]" {
			if payload == "[DONE]" {
				return
			}
			continue
		}
		var f chunkFrame
		if err := json.Unmarshal([]byte(payload), &f); err != nil {
			// Skip unparseable frames — daemon will never emit them but a
			// proxy might splice in keep-alives.
			continue
		}
		switch f.ToriiEvent {
		case "tool_call":
			out <- StreamToolCallMsg{Name: f.Name, Args: f.Arguments}
			continue
		case "tool_result":
			out <- StreamToolResultMsg{Name: f.Name, Output: f.Output, Err: f.Error}
			continue
		case "error":
			out <- StreamErrorMsg{Err: errors.New(f.Message)}
			return
		}
		// Plain OpenAI delta — content fragments only. Role-announce and
		// stop chunks have no content; we ignore them.
		if len(f.Choices) > 0 && f.Choices[0].Delta.Content != "" {
			out <- StreamTokenMsg{Text: f.Choices[0].Delta.Content}
		}
	}
}

// waitForStreamMsg returns a tea.Cmd that blocks on the next event from ch
// and surfaces it as a tea.Msg. The model rearmes the command on every
// non-terminal message; on Done / Error the model lets the chain end.
func waitForStreamMsg(ch <-chan tea.Msg) tea.Cmd {
	return func() tea.Msg {
		m, ok := <-ch
		if !ok {
			// Channel closed without a Done message — treat as Done.
			return StreamDoneMsg{}
		}
		return m
	}
}

// ping verifies the daemon is reachable at the configured base URL before we
// open the TUI. Hits /healthz, which is unauthenticated.
func (c *Client) ping(ctx context.Context) error {
	ctx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, c.BaseURL+"/healthz", nil)
	if err != nil {
		return err
	}
	resp, err := c.http.Do(req)
	if err != nil {
		return err
	}
	defer resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		return fmt.Errorf("healthz returned %d", resp.StatusCode)
	}
	return nil
}
