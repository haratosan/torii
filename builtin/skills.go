package builtin

import (
	"context"
	"database/sql"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/store"
)

type skillsArgs struct {
	Action string `json:"action"`
	Title  string `json:"title"`
	Body   string `json:"body"`
	ID     int64  `json:"id"`
	Global bool   `json:"global"`
}

// userScope returns the scope key for a user. Returns "" if userID is empty
// (e.g. when called outside a user context); callers should reject such calls.
func userScope(userID string) string {
	if userID == "" {
		return ""
	}
	return "user:" + userID
}

// allowedScopes returns the scope keys a request may read. Global skills are
// visible to everyone; user-scoped skills are visible only to their owner.
func allowedScopes(userID string) []string {
	if userID == "" {
		return []string{"global"}
	}
	return []string{"global", "user:" + userID}
}

func NewSkillsTool(db *store.Store) *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "skills",
			Description: "Persistent playbooks for HOW to do recurring tasks and HOW to use tools. Add a skill when you learn a procedure (e.g. 'how user wants standup summaries' or 'when to use the curl tool'). Skills survive /new and model switches and are auto-injected into the system prompt every turn. Default scope is the current user; pass global=true to make a skill visible to all users. Keep each body under ~600 chars; split long procedures into multiple skills.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"add", "update", "remove", "list", "get"},
						"description": "add = create a new skill, update = replace body of existing skill, remove = delete by id, list = list all visible skills, get = read full body of one skill",
					},
					"title": map[string]any{
						"type":        "string",
						"description": "Short identifier for the skill (required for add). Must be unique within the scope.",
					},
					"body": map[string]any{
						"type":        "string",
						"description": "Markdown procedure (required for add and update). Keep under ~600 chars.",
					},
					"id": map[string]any{
						"type":        "integer",
						"description": "Skill id (required for update, remove, get).",
					},
					"global": map[string]any{
						"type":        "boolean",
						"description": "If true on add, the skill is visible to all users; otherwise it is scoped to the current user.",
					},
				},
				"required": []any{"action"},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			var args skillsArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			switch args.Action {
			case "add":
				if strings.TrimSpace(args.Title) == "" || strings.TrimSpace(args.Body) == "" {
					return &extension.ExtResponse{Error: "title and body are required for add"}, nil
				}
				scope := userScope(req.UserID)
				if args.Global {
					scope = "global"
				}
				if scope == "" {
					return &extension.ExtResponse{Error: "no user context — pass global=true to add a global skill"}, nil
				}
				id, err := db.AddSkill(scope, strings.TrimSpace(args.Title), args.Body)
				if err != nil {
					if isUniqueViolation(err) {
						return &extension.ExtResponse{
							Error: fmt.Sprintf("a skill with title %q already exists in scope %q. Use 'skills list' to find its id, then 'skills update' to change its body.", args.Title, scope),
						}, nil
					}
					return nil, err
				}
				return &extension.ExtResponse{
					Output: fmt.Sprintf("Skill #%d added (%s).", id, scope),
				}, nil

			case "update":
				if args.ID <= 0 || strings.TrimSpace(args.Body) == "" {
					return &extension.ExtResponse{Error: "id and body are required for update"}, nil
				}
				// Try user scope first, then global. The scope guard in store
				// keeps this safe — only the owner of the user-scoped skill (or
				// any caller for global) succeeds.
				scopes := allowedScopes(req.UserID)
				var updated bool
				for _, sc := range scopes {
					ok, err := db.UpdateSkill(args.ID, sc, args.Body)
					if err != nil {
						return nil, err
					}
					if ok {
						updated = true
						break
					}
				}
				if !updated {
					return &extension.ExtResponse{Error: fmt.Sprintf("skill #%d not found in your scope", args.ID)}, nil
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("Skill #%d updated.", args.ID)}, nil

			case "remove":
				if args.ID <= 0 {
					return &extension.ExtResponse{Error: "id is required for remove"}, nil
				}
				scopes := allowedScopes(req.UserID)
				var removed bool
				for _, sc := range scopes {
					ok, err := db.RemoveSkill(args.ID, sc)
					if err != nil {
						return nil, err
					}
					if ok {
						removed = true
						break
					}
				}
				if !removed {
					return &extension.ExtResponse{Error: fmt.Sprintf("skill #%d not found in your scope", args.ID)}, nil
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("Skill #%d removed.", args.ID)}, nil

			case "list":
				skills, err := db.ListSkills(allowedScopes(req.UserID))
				if err != nil {
					return nil, err
				}
				if len(skills) == 0 {
					return &extension.ExtResponse{Output: "No skills yet. Use 'skills add' to capture a procedure."}, nil
				}
				var sb strings.Builder
				sb.WriteString(fmt.Sprintf("%d skill(s):\n", len(skills)))
				for _, sk := range skills {
					sb.WriteString(fmt.Sprintf("- #%d [%s] %s (%d chars)\n", sk.ID, sk.Scope, sk.Title, len(sk.Body)))
				}
				return &extension.ExtResponse{Output: sb.String()}, nil

			case "get":
				if args.ID <= 0 {
					return &extension.ExtResponse{Error: "id is required for get"}, nil
				}
				sk, err := db.GetSkill(args.ID, allowedScopes(req.UserID))
				if errors.Is(err, sql.ErrNoRows) {
					return &extension.ExtResponse{Error: fmt.Sprintf("skill #%d not found", args.ID)}, nil
				}
				if err != nil {
					return nil, err
				}
				return &extension.ExtResponse{
					Output: fmt.Sprintf("#%d [%s] %s\n\n%s", sk.ID, sk.Scope, sk.Title, sk.Body),
				}, nil

			default:
				return &extension.ExtResponse{Error: fmt.Sprintf("unknown action: %s", args.Action)}, nil
			}
		},
	}
}

// isUniqueViolation matches modernc.org/sqlite's "UNIQUE constraint failed"
// error string. Keeping this string-based to avoid pulling in driver-specific
// types — fragile but contained.
func isUniqueViolation(err error) bool {
	if err == nil {
		return false
	}
	return strings.Contains(err.Error(), "UNIQUE constraint failed")
}
