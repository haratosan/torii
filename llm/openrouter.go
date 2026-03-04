package llm

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
)

const openRouterURL = "https://openrouter.ai/api/v1/chat/completions"

type OpenRouterProvider struct {
	apiKey string
	model  string
	client *http.Client
	logger *slog.Logger
}

func NewOpenRouter(apiKey, model string, logger *slog.Logger) *OpenRouterProvider {
	return &OpenRouterProvider{
		apiKey: apiKey,
		model:  model,
		client: &http.Client{},
		logger: logger,
	}
}

// OpenAI-compatible request/response types
type orRequest struct {
	Model    string      `json:"model"`
	Messages []orMessage `json:"messages"`
	Tools    []orTool    `json:"tools,omitempty"`
}

type orMessage struct {
	Role       string       `json:"role"`
	Content    string       `json:"content"`
	ToolCalls  []orToolCall `json:"tool_calls,omitempty"`
	ToolCallID string       `json:"tool_call_id,omitempty"`
}

type orTool struct {
	Type     string     `json:"type"`
	Function orFunction `json:"function"`
}

type orFunction struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type orToolCall struct {
	ID       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}

type orResponse struct {
	Choices []struct {
		Message struct {
			Role      string       `json:"role"`
			Content   string       `json:"content"`
			ToolCalls []orToolCall `json:"tool_calls,omitempty"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error,omitempty"`
}

func (o *OpenRouterProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	messages := make([]orMessage, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := orMessage{
			Role:       string(m.Role),
			Content:    m.Content,
			ToolCallID: m.ToolCallID,
		}
		for _, tc := range m.ToolCalls {
			msg.ToolCalls = append(msg.ToolCalls, orToolCall{
				ID:   tc.ID,
				Type: "function",
				Function: struct {
					Name      string `json:"name"`
					Arguments string `json:"arguments"`
				}{
					Name:      tc.Function.Name,
					Arguments: tc.Function.Arguments,
				},
			})
		}
		messages = append(messages, msg)
	}

	var tools []orTool
	for _, t := range req.Tools {
		tools = append(tools, orTool{
			Type: "function",
			Function: orFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters:  t.Parameters,
			},
		})
	}

	orReq := orRequest{
		Model:    o.model,
		Messages: messages,
		Tools:    tools,
	}

	body, err := json.Marshal(orReq)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	httpReq, err := http.NewRequestWithContext(ctx, "POST", openRouterURL, bytes.NewReader(body))
	if err != nil {
		return nil, fmt.Errorf("create request: %w", err)
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+o.apiKey)

	resp, err := o.client.Do(httpReq)
	if err != nil {
		return nil, fmt.Errorf("openrouter request: %w", err)
	}
	defer resp.Body.Close()

	respBody, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, fmt.Errorf("read response: %w", err)
	}

	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("openrouter error (status %d): %s", resp.StatusCode, string(respBody))
	}

	var orResp orResponse
	if err := json.Unmarshal(respBody, &orResp); err != nil {
		return nil, fmt.Errorf("unmarshal response: %w", err)
	}

	if orResp.Error != nil {
		return nil, fmt.Errorf("openrouter api error: %s", orResp.Error.Message)
	}

	if len(orResp.Choices) == 0 {
		return nil, fmt.Errorf("openrouter: no choices in response")
	}

	choice := orResp.Choices[0]
	result := &ChatResponse{
		Content: choice.Message.Content,
	}

	for _, tc := range choice.Message.ToolCalls {
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID: tc.ID,
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name:      tc.Function.Name,
				Arguments: tc.Function.Arguments,
			},
		})
	}

	return result, nil
}
