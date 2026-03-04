package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/store"
	"github.com/robfig/cron/v3"
)

type cronArgs struct {
	Action      string `json:"action"`
	Schedule    string `json:"schedule"`
	Description string `json:"description"`
	ID          int64  `json:"id"`
}

func NewCronTool(db *store.Store) *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "cron",
			Description: "Create, list, or delete recurring scheduled tasks. When triggered, the task description is processed by the AI. Actions: create (new recurring task), list (show tasks), delete (remove task). Schedule uses cron syntax: 'minute hour day-of-month month day-of-week', e.g. '0 9 * * 1' = every Monday 9:00.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"create", "list", "delete"},
						"description": "The action to perform",
					},
					"schedule": map[string]any{
						"type":        "string",
						"description": "Cron expression, e.g. '0 9 * * 1' for every Monday at 9:00 (for create)",
					},
					"description": map[string]any{
						"type":        "string",
						"description": "Task description - this text will be processed by the AI each time the task triggers (for create)",
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
			var args cronArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			switch args.Action {
			case "create":
				if args.Schedule == "" {
					return &extension.ExtResponse{Error: "schedule is required"}, nil
				}
				if args.Description == "" {
					return &extension.ExtResponse{Error: "description is required"}, nil
				}

				nextRun, err := nextCronRun(args.Schedule)
				if err != nil {
					return &extension.ExtResponse{Error: fmt.Sprintf("invalid cron expression: %s", err)}, nil
				}

				task := &store.Task{
					Type:        "cron",
					ChatID:      req.ChatID,
					UserID:      req.UserID,
					Description: args.Description,
					Schedule:    args.Schedule,
					NextRun:     nextRun,
					OneShot:     false,
				}
				if err := db.CreateTask(task); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{
					Output: fmt.Sprintf("Cron task #%d created. Schedule: %s. Next run: %s", task.ID, args.Schedule, nextRun.Format("2006-01-02 15:04")),
				}, nil

			case "list":
				tasks, err := db.ListTasksByChat(req.ChatID)
				if err != nil {
					return nil, err
				}
				var crons []*store.Task
				for _, t := range tasks {
					if t.Type == "cron" {
						crons = append(crons, t)
					}
				}
				if len(crons) == 0 {
					return &extension.ExtResponse{Output: "No active cron tasks."}, nil
				}
				var sb strings.Builder
				for _, t := range crons {
					sb.WriteString(fmt.Sprintf("#%d: [%s] %s (next: %s)\n", t.ID, t.Schedule, t.Description, t.NextRun.Format("2006-01-02 15:04")))
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
					return &extension.ExtResponse{Error: "task not found"}, nil
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("Cron task #%d deleted.", args.ID)}, nil

			default:
				return &extension.ExtResponse{Error: fmt.Sprintf("unknown action: %s", args.Action)}, nil
			}
		},
	}
}

func nextCronRun(schedule string) (time.Time, error) {
	parser := cron.NewParser(cron.Minute | cron.Hour | cron.Dom | cron.Month | cron.Dow)
	sched, err := parser.Parse(schedule)
	if err != nil {
		return time.Time{}, err
	}
	return sched.Next(time.Now()), nil
}
