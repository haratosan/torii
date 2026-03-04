package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/store"
)

type remindArgs struct {
	Action   string `json:"action"`
	Message  string `json:"message"`
	Duration string `json:"duration"`
	ID       int64  `json:"id"`
}

func NewRemindTool(db *store.Store) *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "remind",
			Description: "Create, list, or delete one-shot reminders. The reminder message will be sent directly to the chat when the time comes. Actions: create (set a new reminder), list (show active reminders), delete (remove a reminder).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"create", "list", "delete"},
						"description": "The action to perform",
					},
					"message": map[string]any{
						"type":        "string",
						"description": "Reminder message (for create)",
					},
					"duration": map[string]any{
						"type":        "string",
						"description": "Duration from now, e.g. '5m', '1h', '2h30m', '24h' (for create)",
					},
					"id": map[string]any{
						"type":        "integer",
						"description": "Task ID to delete (for delete)",
					},
				},
				"required": []any{"action"},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			var args remindArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			switch args.Action {
			case "create":
				if args.Message == "" {
					return &extension.ExtResponse{Error: "message is required"}, nil
				}
				if args.Duration == "" {
					return &extension.ExtResponse{Error: "duration is required"}, nil
				}
				dur, err := time.ParseDuration(args.Duration)
				if err != nil {
					return &extension.ExtResponse{Error: fmt.Sprintf("invalid duration: %s", err)}, nil
				}
				nextRun := time.Now().Add(dur)
				task := &store.Task{
					Type:        "remind",
					ChatID:      req.ChatID,
					UserID:      req.UserID,
					Description: args.Message,
					NextRun:     nextRun,
					OneShot:     true,
				}
				if err := db.CreateTask(task); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{
					Output: fmt.Sprintf("Reminder #%d set for %s (%s from now).", task.ID, nextRun.Format("2006-01-02 15:04"), args.Duration),
				}, nil

			case "list":
				tasks, err := db.ListTasksByChat(req.ChatID)
				if err != nil {
					return nil, err
				}
				var reminders []*store.Task
				for _, t := range tasks {
					if t.Type == "remind" {
						reminders = append(reminders, t)
					}
				}
				if len(reminders) == 0 {
					return &extension.ExtResponse{Output: "No active reminders."}, nil
				}
				var sb strings.Builder
				for _, t := range reminders {
					sb.WriteString(fmt.Sprintf("#%d: %s (at %s)\n", t.ID, t.Description, t.NextRun.Format("2006-01-02 15:04")))
				}
				return &extension.ExtResponse{Output: sb.String()}, nil

			case "delete":
				if args.ID == 0 {
					return &extension.ExtResponse{Error: "id is required"}, nil
				}
				deleted, err := db.DeleteTaskByChat(args.ID, req.ChatID)
				if err != nil {
					return nil, err
				}
				if !deleted {
					return &extension.ExtResponse{Error: "reminder not found"}, nil
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("Reminder #%d deleted.", args.ID)}, nil

			default:
				return &extension.ExtResponse{Error: fmt.Sprintf("unknown action: %s", args.Action)}, nil
			}
		},
	}
}
