package builtin

import (
	"context"

	"github.com/haratosan/torii/extension"
)

func NewNoReplyTool() *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "no-reply",
			Description: "Call this tool to suppress sending a message to the user. Use this when a cron task or scheduled check finds nothing noteworthy to report. When you call this tool, no message will be sent.",
			Parameters: map[string]any{
				"type":       "object",
				"properties": map[string]any{},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			return &extension.ExtResponse{Output: "OK, no message will be sent."}, nil
		},
	}
}
