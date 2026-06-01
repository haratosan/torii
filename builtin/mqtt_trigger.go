package builtin

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/mqtt"
	"github.com/haratosan/torii/store"
)

type mqttTriggerArgs struct {
	Action string `json:"action"`
	Name   string `json:"name"`
	Topic  string `json:"topic"`
	Match  string `json:"match"`
	Prompt string `json:"prompt"`
	Silent bool   `json:"silent"`
	ID     int64  `json:"id"`
}

func NewMQTTTriggerTool(db *store.Store, sub *mqtt.Subscriber) *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "mqtt_trigger",
			Description: "Create, list, delete, enable, or disable persistent MQTT triggers. A trigger subscribes to a topic; when a matching message arrives, the agent runs your prompt with the payload attached as data. Actions: create, list, delete, enable, disable. Use this for event-driven automations like 'notify me when someone unlocks the door' (topic e.g. nuki/+/lockActionEvent).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"create", "list", "delete", "enable", "disable"},
						"description": "What to do",
					},
					"name": map[string]any{
						"type":        "string",
						"description": "Short unique name (per user). Required for create; for delete/enable/disable either name or id.",
					},
					"topic": map[string]any{
						"type":        "string",
						"description": "MQTT topic; supports + (single-level) and # (multi-level) wildcards. Required for create. Example: nuki/+/lockActionEvent",
					},
					"match": map[string]any{
						"type":        "string",
						"description": "Optional substring filter on the payload — trigger only fires if the payload contains this string.",
					},
					"prompt": map[string]any{
						"type":        "string",
						"description": "Instructions for the AI when the trigger fires. The MQTT payload is appended automatically as untrusted data. Required for create.",
					},
					"silent": map[string]any{
						"type":        "boolean",
						"description": "If true, never push the AI's response to the chat (the trigger only runs tools silently). Default false.",
					},
					"id": map[string]any{
						"type":        "integer",
						"description": "Trigger ID (for delete/enable/disable as an alternative to name).",
					},
				},
				"required": []any{"action"},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			var args mqttTriggerArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			switch args.Action {
			case "create":
				if args.Name == "" {
					return &extension.ExtResponse{Error: "name is required"}, nil
				}
				if args.Topic == "" {
					return &extension.ExtResponse{Error: "topic is required"}, nil
				}
				if args.Prompt == "" {
					return &extension.ExtResponse{Error: "prompt is required"}, nil
				}
				t := &store.MQTTTrigger{
					Name:    args.Name,
					Topic:   args.Topic,
					Match:   args.Match,
					ChatID:  req.ChatID,
					UserID:  req.UserID,
					Prompt:  args.Prompt,
					Silent:  args.Silent,
					Enabled: true,
				}
				if _, err := db.MQTTTriggerCreate(t); err != nil {
					return &extension.ExtResponse{Error: "create failed: " + err.Error()}, nil
				}
				if sub != nil {
					if err := sub.Register(t); err != nil {
						// Persistence succeeded; live subscribe failed — the
						// subscriber will pick it up on next OnConnect.
						return &extension.ExtResponse{
							Output: fmt.Sprintf("MQTT trigger #%d %q created (topic %s). Live subscribe failed (%s) — will retry on reconnect.", t.ID, t.Name, t.Topic, err),
						}, nil
					}
				}
				return &extension.ExtResponse{
					Output: fmt.Sprintf("MQTT trigger #%d %q created and subscribed to %s.", t.ID, t.Name, t.Topic),
				}, nil

			case "list":
				triggers, err := db.MQTTTriggerListByUser(req.UserID)
				if err != nil {
					return nil, err
				}
				if len(triggers) == 0 {
					return &extension.ExtResponse{Output: "No MQTT triggers."}, nil
				}
				var sb strings.Builder
				for _, t := range triggers {
					state := "enabled"
					if !t.Enabled {
						state = "disabled"
					}
					fmt.Fprintf(&sb, "#%d %q [%s] topic=%s", t.ID, t.Name, state, t.Topic)
					if t.Match != "" {
						fmt.Fprintf(&sb, " match=%q", t.Match)
					}
					if t.Silent {
						sb.WriteString(" silent")
					}
					sb.WriteByte('\n')
				}
				return &extension.ExtResponse{Output: sb.String()}, nil

			case "delete", "enable", "disable":
				id, err := resolveTriggerID(db, req.UserID, args)
				if err != nil {
					return &extension.ExtResponse{Error: err.Error()}, nil
				}
				if args.Action == "delete" {
					ok, err := db.MQTTTriggerDeleteByUser(id, req.UserID)
					if err != nil {
						return nil, err
					}
					if !ok {
						return &extension.ExtResponse{Error: "trigger not found"}, nil
					}
					if sub != nil {
						_ = sub.Unregister(id)
					}
					return &extension.ExtResponse{Output: fmt.Sprintf("MQTT trigger #%d deleted.", id)}, nil
				}
				enable := args.Action == "enable"
				ok, err := db.MQTTTriggerSetEnabledByUser(id, req.UserID, enable)
				if err != nil {
					return nil, err
				}
				if !ok {
					return &extension.ExtResponse{Error: "trigger not found"}, nil
				}
				if sub != nil {
					if enable {
						if t, _ := db.MQTTTriggerGet(id); t != nil {
							_ = sub.Register(t)
						}
					} else {
						_ = sub.Unregister(id)
					}
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("MQTT trigger #%d %sd.", id, args.Action)}, nil

			default:
				return &extension.ExtResponse{Error: fmt.Sprintf("unknown action: %s", args.Action)}, nil
			}
		},
	}
}

// resolveTriggerID accepts either an explicit id or a name (scoped to user)
// and returns the numeric id. Errors if neither is set or the name is unknown.
func resolveTriggerID(db *store.Store, userID string, args mqttTriggerArgs) (int64, error) {
	if args.ID != 0 {
		return args.ID, nil
	}
	if args.Name == "" {
		return 0, fmt.Errorf("either id or name is required")
	}
	triggers, err := db.MQTTTriggerListByUser(userID)
	if err != nil {
		return 0, err
	}
	for _, t := range triggers {
		if t.Name == args.Name {
			return t.ID, nil
		}
	}
	return 0, fmt.Errorf("trigger %q not found", args.Name)
}
