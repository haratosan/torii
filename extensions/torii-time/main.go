package main

import (
	"encoding/json"
	"fmt"
	"os"
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

func main() {
	var req Request
	if err := json.NewDecoder(os.Stdin).Decode(&req); err != nil {
		fmt.Fprintf(os.Stderr, "decode error: %v\n", err)
		os.Exit(1)
	}

	now := time.Now()
	resp := Response{
		Output: now.Format("2006-01-02 15:04:05 MST"),
	}
	json.NewEncoder(os.Stdout).Encode(resp)
}
