package builtin

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/store"
)

type memoryArgs struct {
	Action string `json:"action"`
	Notes  string `json:"notes"`
	Needle string `json:"needle"`
}

func NewMemoryTool(db *store.Store, maxChars int) *extension.BuiltinTool {
	if maxChars <= 0 {
		maxChars = 1500
	}
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "memory",
			Description: "Persistent line-based notes about the current user. PREFER 'add' for new facts (appends a line). Use 'replace' (case-insensitive substring match on `needle`) to update an existing line, 'remove' (substring match) to delete one. NEVER use 'set' unless the user explicitly asks to wipe and rewrite memory — it deletes everything.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"get", "list", "add", "replace", "remove", "set", "delete"},
						"description": "get/list = read all notes, add = append one line, replace = update first line matching `needle`, remove = delete first line matching `needle`, set = DESTRUCTIVE full overwrite, delete = wipe all notes",
					},
					"notes": map[string]any{
						"type":        "string",
						"description": "Single fact for add/replace, or full new blob for set. Internal newlines are collapsed.",
					},
					"needle": map[string]any{
						"type":        "string",
						"description": "Case-insensitive substring used by replace/remove to find the target line.",
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
			case "get", "list":
				lines, err := db.GetMemoryLines(req.UserID)
				if err != nil {
					return nil, err
				}
				if len(lines) == 0 {
					return &extension.ExtResponse{Output: "No notes stored for this user."}, nil
				}
				var sb strings.Builder
				total := 0
				for i, l := range lines {
					sb.WriteString(fmt.Sprintf("%d. %s\n", i+1, l))
					total += len(l)
				}
				// Account for joining newlines so the usage figure matches what
				// the store actually persists.
				if len(lines) > 1 {
					total += len(lines) - 1
				}
				sb.WriteString(fmt.Sprintf("\nusage: %d/%d chars", total, maxChars))
				return &extension.ExtResponse{Output: sb.String()}, nil

			case "add":
				if strings.TrimSpace(args.Notes) == "" {
					return &extension.ExtResponse{Error: "notes is required for add (the fact to append)"}, nil
				}
				newTotal, err := db.AppendMemoryLine(req.UserID, args.Notes, maxChars)
				if errors.Is(err, store.ErrMemoryFull) {
					return &extension.ExtResponse{
						Error: fmt.Sprintf("memory full (%d/%d chars). Use 'memory list' to inspect, then 'memory remove' or 'memory replace' to consolidate before adding more.", newTotal, maxChars),
					}, nil
				}
				if err != nil {
					return nil, err
				}
				return &extension.ExtResponse{
					Output: fmt.Sprintf("Note added. usage: %d/%d chars", newTotal, maxChars),
				}, nil

			case "replace":
				if strings.TrimSpace(args.Needle) == "" {
					return &extension.ExtResponse{Error: "needle is required for replace (substring of the line to update)"}, nil
				}
				if strings.TrimSpace(args.Notes) == "" {
					return &extension.ExtResponse{Error: "notes is required for replace (the new line)"}, nil
				}
				matched, ok, err := db.ReplaceMemoryLine(req.UserID, args.Needle, args.Notes)
				if err != nil {
					return nil, err
				}
				if !ok {
					return &extension.ExtResponse{
						Error: fmt.Sprintf("no line matched needle %q. Use 'memory list' to find the right substring, or 'memory add' to append a new line.", args.Needle),
					}, nil
				}
				return &extension.ExtResponse{
					Output: fmt.Sprintf("Replaced: %q", matched),
				}, nil

			case "remove":
				if strings.TrimSpace(args.Needle) == "" {
					return &extension.ExtResponse{Error: "needle is required for remove (substring of the line to delete)"}, nil
				}
				removed, ok, err := db.RemoveMemoryLine(req.UserID, args.Needle)
				if err != nil {
					return nil, err
				}
				if !ok {
					return &extension.ExtResponse{
						Error: fmt.Sprintf("no line matched needle %q. Use 'memory list' to find the right substring.", args.Needle),
					}, nil
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("Removed: %q", removed)}, nil

			case "set":
				if args.Notes == "" {
					return &extension.ExtResponse{Error: "notes cannot be empty for set action"}, nil
				}
				if err := db.SetMemory(req.UserID, args.Notes); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{
					Output: "Notes overwritten. (Tip: prefer 'add' for new facts and 'replace' for updates so prior notes are not lost.)",
				}, nil

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
