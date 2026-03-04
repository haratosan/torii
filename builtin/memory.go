package builtin

import (
	"context"
	"encoding/json"
	"fmt"

	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/store"
)

type memoryArgs struct {
	Action string `json:"action"`
	Notes  string `json:"notes"`
}

func NewMemoryTool(db *store.Store) *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "memory",
			Description: "Manage persistent notes about the current user. Actions: get (retrieve notes), set (save notes), delete (clear notes). Use this to remember user preferences, names, and other personal information across conversations.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"get", "set", "delete"},
						"description": "The action to perform",
					},
					"notes": map[string]any{
						"type":        "string",
						"description": "The notes to save (for set action). This replaces all existing notes, so include everything you want to remember.",
					},
				},
				"required": []any{"action"},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			var args memoryArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			switch args.Action {
			case "get":
				notes, err := db.GetMemory(req.UserID)
				if err != nil {
					return nil, err
				}
				if notes == "" {
					return &extension.ExtResponse{Output: "No notes stored for this user."}, nil
				}
				return &extension.ExtResponse{Output: notes}, nil

			case "set":
				if args.Notes == "" {
					return &extension.ExtResponse{Error: "notes cannot be empty for set action"}, nil
				}
				if err := db.SetMemory(req.UserID, args.Notes); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{Output: "Notes saved."}, nil

			case "delete":
				if err := db.DeleteMemory(req.UserID); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{Output: "Notes deleted."}, nil

			default:
				return &extension.ExtResponse{Error: fmt.Sprintf("unknown action: %s", args.Action)}, nil
			}
		},
	}
}
