package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
	"regexp"
	"strings"
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

const maxOutputBytes = 50 * 1024

var (
	scriptRe   = regexp.MustCompile(`(?is)<script\b[^>]*>.*?</script>`)
	styleRe    = regexp.MustCompile(`(?is)<style\b[^>]*>.*?</style>`)
	noscriptRe = regexp.MustCompile(`(?is)<noscript\b[^>]*>.*?</noscript>`)
	commentRe  = regexp.MustCompile(`(?s)<!--.*?-->`)
	tagRe      = regexp.MustCompile(`(?s)<[^>]+>`)
	titleRe    = regexp.MustCompile(`(?is)<title\b[^>]*>(.*?)</title>`)
	wsRe       = regexp.MustCompile(`[ \t]+`)
	blanklineRe = regexp.MustCompile(`\n{3,}`)
)

func reply(r Response) {
	if r.Data == nil {
		r.Data = map[string]any{}
	}
	_ = json.NewEncoder(os.Stdout).Encode(r)
	os.Exit(0)
}

func extractTitle(html string) string {
	m := titleRe.FindStringSubmatch(html)
	if len(m) < 2 {
		return ""
	}
	return strings.TrimSpace(decodeEntities(m[1]))
}

func htmlToText(html string) string {
	html = scriptRe.ReplaceAllString(html, "")
	html = styleRe.ReplaceAllString(html, "")
	html = noscriptRe.ReplaceAllString(html, "")
	html = commentRe.ReplaceAllString(html, "")
	// Convert common block tags to newlines so paragraphs survive
	blockRe := regexp.MustCompile(`(?i)</(p|div|br|li|tr|h[1-6]|section|article|header|footer|blockquote)\s*>`)
	html = blockRe.ReplaceAllString(html, "\n")
	html = tagRe.ReplaceAllString(html, "")
	html = decodeEntities(html)
	// Normalize whitespace
	html = strings.ReplaceAll(html, "\r", "")
	html = wsRe.ReplaceAllString(html, " ")
	lines := strings.Split(html, "\n")
	out := make([]string, 0, len(lines))
	for _, l := range lines {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	text := strings.Join(out, "\n")
	text = blanklineRe.ReplaceAllString(text, "\n\n")
	return text
}

func decodeEntities(s string) string {
	r := strings.NewReplacer(
		"&nbsp;", " ",
		"&amp;", "&",
		"&lt;", "<",
		"&gt;", ">",
		"&quot;", `"`,
		"&#39;", "'",
		"&apos;", "'",
		"&hellip;", "…",
		"&mdash;", "—",
		"&ndash;", "–",
	)
	return r.Replace(s)
}

func main() {
	var req Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fmt.Fprintf(os.Stderr, "decode error: %v\n", err)
		os.Exit(1)
	}

	var params struct {
		URL string `json:"url"`
	}
	if err := json.Unmarshal([]byte(req.Input), &params); err != nil {
		reply(Response{Error: fmt.Sprintf("invalid input: %v", err)})
	}

	if params.URL == "" {
		reply(Response{Error: "url is required"})
	}

	if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
		params.URL = "https://" + params.URL
	}

	httpReq, err := http.NewRequest("GET", params.URL, nil)
	if err != nil {
		reply(Response{Error: fmt.Sprintf("request error: %v", err)})
	}
	// Some sites 403 the default Go UA
	httpReq.Header.Set("User-Agent", "Mozilla/5.0 (compatible; ToriiBot/0.1; +https://github.com/haratosan/torii)")

	resp, err := http.DefaultClient.Do(httpReq)
	if err != nil {
		reply(Response{Error: fmt.Sprintf("fetch error: %v", err)})
	}
	defer resp.Body.Close()

	// Read up to ~1 MB raw, then extract — limit final output to 50 KB
	raw, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		reply(Response{Error: fmt.Sprintf("read error: %v", err)})
	}

	contentType := resp.Header.Get("Content-Type")
	body := string(raw)
	title := ""
	output := body

	if strings.Contains(strings.ToLower(contentType), "html") || strings.Contains(strings.ToLower(body[:min(len(body), 200)]), "<html") {
		title = extractTitle(body)
		output = htmlToText(body)
	}

	if len(output) > maxOutputBytes {
		output = output[:maxOutputBytes] + "\n…(truncated)"
	}

	reply(Response{
		Output: output,
		Data: map[string]any{
			"status_code":  resp.StatusCode,
			"url":          params.URL,
			"title":        title,
			"content_type": contentType,
		},
	})
}
