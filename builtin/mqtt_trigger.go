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

	// Update-only: presence flags so callers can clear a field (e.g. match)
	// without confusing "empty string means unchanged".
	SetTopic  bool `json:"set_topic"`
	SetMatch  bool `json:"set_match"`
	SetPrompt bool `json:"set_prompt"`
	SetSilent bool `json:"set_silent"`
}

func NewMQTTTriggerTool(db *store.Store, sub *mqtt.Subscriber) *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "mqtt_trigger",
			Description: "Create, list, get, update, delete, enable, or disable persistent MQTT triggers. A trigger subscribes to a topic; when a matching message arrives, the agent runs your prompt with the payload attached as data. Use this for event-driven automations like 'notify me when someone unlocks the door' (topic e.g. nuki/+/lockActionEvent). Use `get` to read a trigger's full prompt; `update` to change topic/match/prompt/silent without re-creating (preserves the id).",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"action": map[string]any{
						"type":        "string",
						"enum":        []any{"create", "list", "get", "update", "delete", "enable", "disable"},
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
						"description": "Trigger ID (for get/update/delete/enable/disable as an alternative to name).",
					},
					"set_topic": map[string]any{
						"type":        "boolean",
						"description": "Update-only: set true together with `topic` to change the topic. Without this flag, `topic` is ignored on update.",
					},
					"set_match": map[string]any{
						"type":        "boolean",
						"description": "Update-only: set true together with `match` to change the payload filter (use empty string to clear it).",
					},
					"set_prompt": map[string]any{
						"type":        "boolean",
						"description": "Update-only: set true together with `prompt` to change the trigger instructions.",
					},
					"set_silent": map[string]any{
						"type":        "boolean",
						"description": "Update-only: set true together with `silent` to change the silent flag.",
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

			case "get":
				id, err := resolveTriggerID(db, req.UserID, args)
				if err != nil {
					return &extension.ExtResponse{Error: err.Error()}, nil
				}
				t, err := db.MQTTTriggerGet(id)
				if err != nil {
					return nil, err
				}
				if t == nil || t.UserID != req.UserID {
					return &extension.ExtResponse{Error: "trigger not found"}, nil
				}
				state := "enabled"
				if !t.Enabled {
					state = "disabled"
				}
				var sb strings.Builder
				fmt.Fprintf(&sb, "id: %d\nname: %s\nstate: %s\ntopic: %s\n", t.ID, t.Name, state, t.Topic)
				if t.Match != "" {
					fmt.Fprintf(&sb, "match: %s\n", t.Match)
				}
				if t.Silent {
					sb.WriteString("silent: true\n")
				}
				fmt.Fprintf(&sb, "prompt: |\n%s\n", indent(t.Prompt, "  "))
				return &extension.ExtResponse{Output: sb.String()}, nil

			case "update":
				id, err := resolveTriggerID(db, req.UserID, args)
				if err != nil {
					return &extension.ExtResponse{Error: err.Error()}, nil
				}
				var topicPtr, matchPtr, promptPtr *string
				var silentPtr *bool
				if args.SetTopic {
					t := args.Topic
					topicPtr = &t
				}
				if args.SetMatch {
					m := args.Match
					matchPtr = &m
				}
				if args.SetPrompt {
					p := args.Prompt
					promptPtr = &p
				}
				if args.SetSilent {
					s := args.Silent
					silentPtr = &s
				}
				if topicPtr == nil && matchPtr == nil && promptPtr == nil && silentPtr == nil {
					return &extension.ExtResponse{Error: "no fields to update — pass set_topic/set_match/set_prompt/set_silent together with the value"}, nil
				}
				oldTopic, updated, ok, err := db.MQTTTriggerUpdateByUser(id, req.UserID, topicPtr, matchPtr, promptPtr, silentPtr)
				if err != nil {
					return nil, err
				}
				if !ok {
					return &extension.ExtResponse{Error: "trigger not found"}, nil
				}
				if sub != nil && updated.Enabled && oldTopic != updated.Topic {
					if err := sub.Resubscribe(updated, oldTopic); err != nil {
						return &extension.ExtResponse{
							Output: fmt.Sprintf("MQTT trigger #%d updated, but live resubscribe failed (%s) — will pick up on next reconnect.", id, err),
						}, nil
					}
				}
				return &extension.ExtResponse{Output: fmt.Sprintf("MQTT trigger #%d updated.", id)}, nil

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

func indent(s, prefix string) string {
	if s == "" {
		return ""
	}
	lines := strings.Split(s, "\n")
	for i, l := range lines {
		lines[i] = prefix + l
	}
	return strings.Join(lines, "\n")
}
