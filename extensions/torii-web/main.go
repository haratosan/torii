package main

import (
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"os"
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
		json.NewEncoder(os.Stdout).Encode(Response{Error: fmt.Sprintf("invalid input: %v", err)})
		os.Exit(0)
	}

	if params.URL == "" {
		json.NewEncoder(os.Stdout).Encode(Response{Error: "url is required"})
		os.Exit(0)
	}

	// Ensure URL has a scheme
	if !strings.HasPrefix(params.URL, "http://") && !strings.HasPrefix(params.URL, "https://") {
		params.URL = "https://" + params.URL
	}

	resp, err := http.Get(params.URL)
	if err != nil {
		json.NewEncoder(os.Stdout).Encode(Response{Error: fmt.Sprintf("fetch error: %v", err)})
		os.Exit(0)
	}
	defer resp.Body.Close()

	// Limit body to 50KB
	body, err := io.ReadAll(io.LimitReader(resp.Body, 50*1024))
	if err != nil {
		json.NewEncoder(os.Stdout).Encode(Response{Error: fmt.Sprintf("read error: %v", err)})
		os.Exit(0)
	}

	json.NewEncoder(os.Stdout).Encode(Response{
		Output: string(body),
		Data: map[string]any{
			"status_code": resp.StatusCode,
			"url":         params.URL,
		},
	})
}
