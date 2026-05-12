package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/haratosan/torii/api"
	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/store"
)

type apiAdminArgs struct {
	Action       string `json:"action"`
	ID           int64  `json:"id"`
	Name         string `json:"name"`
	Tool         string `json:"tool"`
	TelegramUser string `json:"telegram_user"`
}

// NewAPIAdminTool builds the admin-only `api-admin` builtin. The handler
// hard-checks UserID against adminUserID — even if the LLM somehow exposes
// the tool to a non-admin caller, the handler refuses. adminUserID == "" is
// treated as "no admin configured" and every call is denied.
func NewAPIAdminTool(db *store.Store, adminUserID string) *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name: "api-admin",
			Description: "ADMIN-ONLY: manage API users for the OpenAI-compatible HTTP endpoint. " +
				"Refused for non-admin callers. Actions: " +
				"list (show all api users), " +
				"create (name) → returns bearer token ONCE — never retrievable later, " +
				"link (id, telegram_user) — share memory/skills with a Telegram user; pass empty telegram_user to unlink, " +
				"enable/disable (id), " +
				"rotate (id) → new bearer token, " +
				"delete (id) — irreversible, " +
				"grant (id, tool) — add tool to allowlist, " +
				"revoke (id, tool) — remove from allowlist, " +
				"tools (id) — show granted tools.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"list", "create", "link", "enable", "disable", "rotate", "delete", "grant", "revoke", "tools"},
						"description": "Action to perform.",
					},
					"id":   map[string]any{"type": "integer", "description": "API user id (required for everything except list/create)"},
					"name": map[string]any{"type": "string", "description": "Display name (required for create; must be unique)"},
					"tool": map[string]any{"type": "string", "description": "Tool name (required for grant/revoke), e.g. 'memory', 'skills', 'web_search'"},
					"telegram_user": map[string]any{
						"type":        "string",
						"description": "Telegram user id (string) for link action. Empty string unlinks.",
					},
				},
				"required": []any{"action"},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			if adminUserID == "" {
				return &extension.ExtResponse{Error: "api-admin is disabled (no admin_user_id configured)"}, nil
			}
			if req.UserID != adminUserID {
				return &extension.ExtResponse{Error: "api-admin is restricted to the configured Telegram admin"}, nil
			}

			var args apiAdminArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			switch args.Action {
			case "list":
				users, err := db.ListAPIUsers()
				if err != nil {
					return nil, err
				}
				if len(users) == 0 {
					return &extension.ExtResponse{Output: "No API users configured."}, nil
				}
				var sb strings.Builder
				for _, u := range users {
					tools, _ := db.GetAPIUserTools(u.ID)
					linked := u.LinkedTelegramUserID
					if linked == "" {
						linked = "—"
					}
					status := "enabled"
					if !u.Enabled {
						status = "DISABLED"
					}
					sb.WriteString(fmt.Sprintf("#%d %s [%s] linked=%s tools=%d (%s)\n",
						u.ID, u.Name, status, linked, len(tools), strings.Join(tools, ",")))
				}
				return &extension.ExtResponse{Output: sb.String()}, nil

			case "create":
				if strings.TrimSpace(args.Name) == "" {
					return &extension.ExtResponse{Error: "name is required for create"}, nil
				}
				token := api.NewBearerToken()
				u, err := db.CreateAPIUser(args.Name, token)
				if err != nil {
					if strings.Contains(err.Error(), "UNIQUE constraint failed") {
						return &extension.ExtResponse{Error: fmt.Sprintf("an API user named %q already exists", args.Name)}, nil
					}
					return nil, err
				}
				// Token must be visible exactly once — surface it in the
				// output, formatted so it's easy to copy from Telegram.
				return &extension.ExtResponse{
					Output: fmt.Sprintf(
						"Created API user #%d (%s).\n\nBearer token (store this NOW — cannot be retrieved later):\n```\n%s\n```\n\nUse `api-admin grant id=%d tool=<name>` to authorize tools.",
						u.ID, u.Name, token, u.ID,
					),
				}, nil

			case "link":
				if args.ID == 0 {
					return &extension.ExtResponse{Error: "id is required for link"}, nil
				}
				if err := db.UpdateAPIUserLinkedTelegram(args.ID, args.TelegramUser); err != nil {
					return nil, err
				}
				if args.TelegramUser == "" {
					return &extension.ExtResponse{Output: fmt.Sprintf("API user #%d unlinked.", args.ID)}, nil
				}
				return &extension.ExtResponse{
					Output: fmt.Sprintf("API user #%d linked to Telegram user %s — memory/skills are now shared.", args.ID, args.TelegramUser),
				}, nil

			case "enable":
				if args.ID == 0 {
					return &extension.ExtResponse{Error: "id is required for enable"}, nil
				}
				if err := db.SetAPIUserEnabled(args.ID, true); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("API user #%d enabled.", args.ID)}, nil

			case "disable":
				if args.ID == 0 {
					return &extension.ExtResponse{Error: "id is required for disable"}, nil
				}
				if err := db.SetAPIUserEnabled(args.ID, false); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("API user #%d disabled — bearer token now rejects with 401.", args.ID)}, nil

			case "rotate":
				if args.ID == 0 {
					return &extension.ExtResponse{Error: "id is required for rotate"}, nil
				}
				token := api.NewBearerToken()
				if err := db.RotateAPIToken(args.ID, token); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{
					Output: fmt.Sprintf(
						"API user #%d token rotated.\n\nNew bearer token (old one now invalid):\n```\n%s\n```",
						args.ID, token,
					),
				}, nil

			case "delete":
				if args.ID == 0 {
					return &extension.ExtResponse{Error: "id is required for delete"}, nil
				}
				if err := db.DeleteAPIUser(args.ID); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("API user #%d deleted (tool grants cascaded).", args.ID)}, nil

			case "grant":
				if args.ID == 0 || strings.TrimSpace(args.Tool) == "" {
					return &extension.ExtResponse{Error: "id and tool are required for grant"}, nil
				}
				if err := db.GrantAPITool(args.ID, args.Tool); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("Granted tool %q to API user #%d.", args.Tool, args.ID)}, nil

			case "revoke":
				if args.ID == 0 || strings.TrimSpace(args.Tool) == "" {
					return &extension.ExtResponse{Error: "id and tool are required for revoke"}, nil
				}
				if err := db.RevokeAPITool(args.ID, args.Tool); err != nil {
					return nil, err
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("Revoked tool %q from API user #%d.", args.Tool, args.ID)}, nil

			case "tools":
				if args.ID == 0 {
					return &extension.ExtResponse{Error: "id is required for tools"}, nil
				}
				tools, err := db.GetAPIUserTools(args.ID)
				if err != nil {
					return nil, err
				}
				if len(tools) == 0 {
					return &extension.ExtResponse{Output: fmt.Sprintf("API user #%d has no tools granted.", args.ID)}, nil
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("API user #%d tools: %s", args.ID, strings.Join(tools, ", "))}, nil

			default:
				return &extension.ExtResponse{Error: fmt.Sprintf("unknown action: %s", args.Action)}, nil
			}
		},
	}
}
