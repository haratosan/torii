package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/store"
)

type botProfileArgs struct {
	Action string `json:"action"`
	Key    string `json:"key"`
	Value  string `json:"value"`
}

func NewBotProfileTool(db *store.Store) *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "bot-profile",
			Description: "Get or set the bot's identity. Actions: get (show current profile), set (update a profile key). Keys: name, personality, system_prompt.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"get", "set"},
						"description": "The action to perform",
					},
					"key": map[string]any{
						"type":        "string",
						"enum":        []any{"name", "personality", "system_prompt"},
						"description": "Profile key to set",
					},
					"value": map[string]any{
						"type":        "string",
						"description": "New value for the key",
					},
				},
				"required": []any{"action"},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			var args botProfileArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			switch args.Action {
			case "get":
				profile, err := db.GetAllBotProfile()
				if err != nil {
					return nil, err
				}
				if len(profile) == 0 {
					return &extension.ExtResponse{Output: "No bot profile set. Using defaults."}, nil
				}
				var sb strings.Builder
				for k, v := range profile {
					sb.WriteString(fmt.Sprintf("%s: %s\n", k, v))
				}
				return &extension.ExtResponse{Output: sb.String()}, nil

			case "set":
				if args.Key == "" || args.Value == "" {
					return &extension.ExtResponse{Error: "key and value are required for set action"}, nil
				}
				if args.Key != "name" && args.Key != "personality" && args.Key != "system_prompt" {
					return &extension.ExtResponse{Error: fmt.Sprintf("invalid key: %s (allowed: name, personality, system_prompt)", args.Key)}, nil
				}
				if err := db.SetBotProfile(args.Key, args.Value); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("Bot %s updated to: %s", args.Key, args.Value)}, nil

			default:
				return &extension.ExtResponse{Error: fmt.Sprintf("unknown action: %s", args.Action)}, nil
			}
		},
	}
}
