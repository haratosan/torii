package llm

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"strings"

	"github.com/ollama/ollama/api"
)

type OllamaProvider struct {
	client      *api.Client
	model       string
	visionModel string
	logger      *slog.Logger
}

// SetVisionModel configures a fallback model for requests that contain images
// when the primary model does not support image input.
func (o *OllamaProvider) SetVisionModel(model string) {
	o.visionModel = model
}

func NewOllama(host, model string, logger *slog.Logger) (*OllamaProvider, error) {
	// Ollama client reads OLLAMA_HOST env var
	if host != "" {
		os.Setenv("OLLAMA_HOST", host)
	}
	client, err := api.ClientFromEnvironment()
	if err != nil {
		return nil, fmt.Errorf("ollama client: %w", err)
	}
	return &OllamaProvider{client: client, model: model, logger: logger}, nil
}

func (o *OllamaProvider) Chat(ctx context.Context, req ChatRequest) (*ChatResponse, error) {
	messages := make([]api.Message, 0, len(req.Messages))
	for _, m := range req.Messages {
		msg := api.Message{
			Role:    string(m.Role),
			Content: m.Content,
		}
		for _, img := range m.Images {
			msg.Images = append(msg.Images, api.ImageData(img))
		}
		if m.ToolCallID != "" {
			msg.Role = "tool"
		}
		if len(m.ToolCalls) > 0 {
			msg.ToolCalls = make([]api.ToolCall, 0, len(m.ToolCalls))
			for _, tc := range m.ToolCalls {
				args := api.NewToolCallFunctionArguments()
				var argsMap map[string]any
				_ = json.Unmarshal([]byte(tc.Function.Arguments), &argsMap)
				for k, v := range argsMap {
					args.Set(k, v)
				}
				msg.ToolCalls = append(msg.ToolCalls, api.ToolCall{
					Function: api.ToolCallFunction{
						Name:      tc.Function.Name,
						Arguments: args,
					},
				})
			}
		}
		messages = append(messages, msg)
	}

	var tools []api.Tool
	for _, t := range req.Tools {
		props := map[string]any{}
		var required []string
		if p, ok := t.Parameters["properties"]; ok {
			if pm, ok := p.(map[string]any); ok {
				props = pm
			}
		}
		if r, ok := t.Parameters["required"]; ok {
			if ra, ok := r.([]any); ok {
				for _, v := range ra {
					if s, ok := v.(string); ok {
						required = append(required, s)
					}
				}
			}
		}

		toolProps := api.NewToolPropertiesMap()
		for k, v := range props {
			if vm, ok := v.(map[string]any); ok {
				tp := api.ToolProperty{}
				if typStr, ok := vm["type"].(string); ok {
					tp.Type = api.PropertyType{typStr}
				}
				if d, ok := vm["description"].(string); ok {
					tp.Description = d
				}
				toolProps.Set(k, tp)
			}
		}

		tools = append(tools, api.Tool{
			Type: "function",
			Function: api.ToolFunction{
				Name:        t.Name,
				Description: t.Description,
				Parameters: api.ToolFunctionParameters{
					Type:       "object",
					Required:   required,
					Properties: toolProps,
				},
			},
		})
	}

	chatReq := &api.ChatRequest{
		Model:    o.model,
		Messages: messages,
		Tools:    tools,
		Stream:   new(bool), // false = no streaming
	}

	var resp *api.ChatResponse
	err := o.client.Chat(ctx, chatReq, func(cr api.ChatResponse) error {
		resp = &cr
		return nil
	})
	if err != nil {
		// If the main model can't handle images, retry with the vision model
		if o.visionModel != "" && strings.Contains(err.Error(), "does not support image") {
			o.logger.Info("retrying with vision model", "model", o.visionModel)
			chatReq.Model = o.visionModel
			chatReq.Tools = nil // vision model should just describe the image, not call tools
			resp = nil
			err = o.client.Chat(ctx, chatReq, func(cr api.ChatResponse) error {
				resp = &cr
				return nil
			})
			if err != nil {
				return nil, fmt.Errorf("ollama chat (vision fallback): %w", err)
			}
		} else {
			return nil, fmt.Errorf("ollama chat: %w", err)
		}
	}

	result := &ChatResponse{
		Content: resp.Message.Content,
	}

	for _, tc := range resp.Message.ToolCalls {
		argsJSON, _ := json.Marshal(tc.Function.Arguments)
		result.ToolCalls = append(result.ToolCalls, ToolCall{
			ID: tc.Function.Name, // Ollama doesn't have tool call IDs, use name
			Function: struct {
				Name      string `json:"name"`
				Arguments string `json:"arguments"`
			}{
				Name:      tc.Function.Name,
				Arguments: string(argsJSON),
			},
		})
	}

	return result, nil
}
