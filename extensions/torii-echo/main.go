package main

import (
	"encoding/json"
	"fmt"
	"os"
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

	// Parse input parameters
	var params struct {
		Text string `json:"text"`
	}
	if err := json.Unmarshal([]byte(req.Input), &params); err != nil {
		resp := Response{Error: fmt.Sprintf("invalid input: %v", err)}
		json.NewEncoder(os.Stdout).Encode(resp)
		os.Exit(0)
	}

	resp := Response{
		Output: fmt.Sprintf("Echo: %s", params.Text),
	}
	json.NewEncoder(os.Stdout).Encode(resp)
}
