package builtin

import (
	"context"
	"encoding/json"

	"github.com/haratosan/torii/extension"
)

type buttonsArgs struct {
	Message string           `json:"message"`
	Buttons [][]buttonOption `json:"buttons"`
}

type buttonOption struct {
	Text  string `json:"text"`
	Value string `json:"value"`
}

func NewButtonsTool() *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "send-buttons",
			Description: "Send a message with inline buttons to the user. The user can click a button to respond. Use this when presenting options or choices.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"message": map[string]any{
						"type":        "string",
						"description": "The message text to display above the buttons",
					},
					"buttons": map[string]any{
						"type":        "array",
						"description": "Rows of buttons. Each row is an array of button objects with 'text' (display label) and 'value' (short callback data, max 64 bytes)",
						"items": map[string]any{
							"type": "array",
							"items": map[string]any{
								"type": "object",
								"properties": map[string]any{
									"text": map[string]any{
										"type":        "string",
										"description": "Button label shown to user",
									},
									"value": map[string]any{
										"type":        "string",
										"description": "Callback data sent when clicked (max 64 bytes)",
									},
								},
								"required": []any{"text", "value"},
							},
						},
					},
				},
				"required": []any{"message", "buttons"},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			var args buttonsArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			if args.Message == "" {
				return &extension.ExtResponse{Error: "message is required"}, nil
			}
			if len(args.Buttons) == 0 {
				return &extension.ExtResponse{Error: "at least one row of buttons is required"}, nil
			}

			// Convert to generic data for gateway to pick up
			buttonData := make([][]map[string]string, len(args.Buttons))
			for i, row := range args.Buttons {
				buttonData[i] = make([]map[string]string, len(row))
				for j, btn := range row {
					buttonData[i][j] = map[string]string{
						"text":  btn.Text,
						"value": btn.Value,
					}
				}
			}

			return &extension.ExtResponse{
				Output: args.Message,
				Data: map[string]any{
					"buttons": buttonData,
				},
			}, nil
		},
	}
}
