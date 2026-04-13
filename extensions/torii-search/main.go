package main

import (
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type Request struct {
	Action string `json:"action"`
	Input  string `json:"input"`
	ChatID string `json:"chat_id"`
	UserID string `json:"user_id"`
}

type Response struct {
	Output string         `json:"output"`
	Error  string         `json:"error"`
	Data   map[string]any `json:"data"`
}

type Params struct {
	Query      string `json:"query"`
	MaxResults int    `json:"max_results"`
	Depth      string `json:"depth"`
}

type tavilyRequest struct {
	APIKey            string `json:"api_key"`
	Query             string `json:"query"`
	MaxResults        int    `json:"max_results"`
	SearchDepth       string `json:"search_depth"`
	IncludeRawContent bool   `json:"include_raw_content"`
}

type tavilyResult struct {
	Title      string  `json:"title"`
	URL        string  `json:"url"`
	Content    string  `json:"content"`
	RawContent string  `json:"raw_content"`
	Score      float64 `json:"score"`
}

type tavilyResponse struct {
	Query   string         `json:"query"`
	Results []tavilyResult `json:"results"`
	Answer  string         `json:"answer"`
}

const (
	tavilyEndpoint = "https://api.tavily.com/search"
	maxOutputBytes = 8 * 1024
	maxRawExcerpt  = 1500
)

func reply(r Response) {
	if r.Data == nil {
		r.Data = map[string]any{}
	}
	_ = json.NewEncoder(os.Stdout).Encode(r)
	os.Exit(0)
}

func main() {
	var req Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fmt.Fprintf(os.Stderr, "decode error: %v\n", err)
		os.Exit(1)
	}

	var p Params
	if err := json.Unmarshal([]byte(req.Input), &p); err != nil {
		reply(Response{Error: fmt.Sprintf("invalid input: %v", err)})
	}
	if strings.TrimSpace(p.Query) == "" {
		reply(Response{Error: "query is required"})
	}
	if p.MaxResults <= 0 || p.MaxResults > 10 {
		p.MaxResults = 5
	}
	if p.Depth != "advanced" {
		p.Depth = "basic"
	}

	apiKey := os.Getenv("TAVILY_API_KEY")
	if apiKey == "" {
		reply(Response{Error: "TAVILY_API_KEY is not set — get a free key at https://tavily.com and add it to extensions.env in config.yaml"})
	}

	body, _ := json.Marshal(tavilyRequest{
		APIKey:            apiKey,
		Query:             p.Query,
		MaxResults:        p.MaxResults,
		SearchDepth:       p.Depth,
		IncludeRawContent: p.Depth == "advanced",
	})

	client := &http.Client{Timeout: 25 * time.Second}
	httpReq, err := http.NewRequest("POST", tavilyEndpoint, bytes.NewReader(body))
	if err != nil {
		reply(Response{Error: fmt.Sprintf("request build error: %v", err)})
	}
	httpReq.Header.Set("Content-Type", "application/json")

	resp, err := client.Do(httpReq)
	if err != nil {
		reply(Response{Error: fmt.Sprintf("tavily request error: %v", err)})
	}
	defer resp.Body.Close()

	raw, _ := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if resp.StatusCode != 200 {
		reply(Response{Error: fmt.Sprintf("tavily HTTP %d: %s", resp.StatusCode, strings.TrimSpace(string(raw)))})
	}

	var tr tavilyResponse
	if err := json.Unmarshal(raw, &tr); err != nil {
		reply(Response{Error: fmt.Sprintf("tavily decode error: %v", err)})
	}

	if len(tr.Results) == 0 {
		reply(Response{
			Output: fmt.Sprintf("No results for query: %q", p.Query),
			Data:   map[string]any{"query": p.Query, "count": 0},
		})
	}

	var sb strings.Builder
	if tr.Answer != "" {
		sb.WriteString("**Summary:** ")
		sb.WriteString(tr.Answer)
		sb.WriteString("\n\n")
	}
	for i, r := range tr.Results {
		fmt.Fprintf(&sb, "%d. **%s** — %s\n", i+1, strings.TrimSpace(r.Title), r.URL)
		if snippet := strings.TrimSpace(r.Content); snippet != "" {
			sb.WriteString("   ")
			sb.WriteString(snippet)
			sb.WriteString("\n")
		}
		if p.Depth == "advanced" {
			if excerpt := strings.TrimSpace(r.RawContent); excerpt != "" {
				if len(excerpt) > maxRawExcerpt {
					excerpt = excerpt[:maxRawExcerpt] + "…"
				}
				sb.WriteString("   > ")
				sb.WriteString(strings.ReplaceAll(excerpt, "\n", " "))
				sb.WriteString("\n")
			}
		}
		sb.WriteString("\n")
		if sb.Len() > maxOutputBytes {
			break
		}
	}

	output := sb.String()
	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes] + "\n…(truncated)"
	}

	reply(Response{
		Output: output,
		Data: map[string]any{
			"query": p.Query,
			"count": len(tr.Results),
		},
	})
}
