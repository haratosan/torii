// Package api implements an OpenAI-compatible HTTP server that surfaces
// torii's agentic loop to clients like OpenWebUI. Tools, memory, and skills
// run internally — clients see only the final assistant response.
package api

// ChatMessage mirrors the OpenAI request/response message shape. Only `role`
// and `content` are honored on input; tool_calls/name are accepted but
// ignored (tools are torii-internal in this API).
type ChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
	Name    string `json:"name,omitempty"`
}

// ChatCompletionRequest is the shape OpenWebUI / curl / OpenAI SDK clients
// send to /v1/chat/completions. Fields not used by torii are accepted but
// ignored so OpenAI-typed clients pass without modification.
type ChatCompletionRequest struct {
	Model       string        `json:"model"`
	Messages    []ChatMessage `json:"messages"`
	Stream      bool          `json:"stream"`
	Temperature *float64      `json:"temperature,omitempty"`
	MaxTokens   *int          `json:"max_tokens,omitempty"`
}

// ChatCompletionChoice is one element of the `choices` array. Always one for
// torii — we don't do n>1.
type ChatCompletionChoice struct {
	Index        int         `json:"index"`
	Message      ChatMessage `json:"message"`
	FinishReason string      `json:"finish_reason"`
}

// ChatCompletionUsage is reported as zeros for now (Ollama doesn't surface
// token counts cleanly). Field present so OpenAI clients don't break on
// missing keys.
type ChatCompletionUsage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

// ChatCompletionResponse is the non-streaming /v1/chat/completions response.
type ChatCompletionResponse struct {
	ID      string                 `json:"id"`
	Object  string                 `json:"object"`
	Created int64                  `json:"created"`
	Model   string                 `json:"model"`
	Choices []ChatCompletionChoice `json:"choices"`
	Usage   ChatCompletionUsage    `json:"usage"`
}

// ChatCompletionChunk is one SSE frame in streaming mode. The OpenAI spec
// expects the same id across all chunks of a single response.
type ChatCompletionChunk struct {
	ID      string                      `json:"id"`
	Object  string                      `json:"object"`
	Created int64                       `json:"created"`
	Model   string                      `json:"model"`
	Choices []ChatCompletionChunkChoice `json:"choices"`
}

// ChatCompletionChunkChoice carries an incremental delta. `Delta.Content` is
// the new text fragment; `FinishReason` is non-null only on the final chunk.
type ChatCompletionChunkChoice struct {
	Index        int                `json:"index"`
	Delta        ChatCompletionDelta `json:"delta"`
	FinishReason *string            `json:"finish_reason"`
}

type ChatCompletionDelta struct {
	Role    string `json:"role,omitempty"`
	Content string `json:"content,omitempty"`
}

// Model is one entry in /v1/models's `data` array.
type Model struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	OwnedBy string `json:"owned_by"`
}

// ModelsResponse is the /v1/models payload.
type ModelsResponse struct {
	Object string  `json:"object"`
	Data   []Model `json:"data"`
}

// errorResponse mirrors OpenAI's error envelope so clients can parse it.
type errorResponse struct {
	Error struct {
		Message string `json:"message"`
		Type    string `json:"type"`
	} `json:"error"`
}
