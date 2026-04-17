package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"os/exec"

	"github.com/google/shlex"
	"github.com/haratosan/torii/config"
	"github.com/haratosan/torii/extension"
)

type shellArgs struct {
	Command string `json:"command"`
}

func NewShellTool(cfg *config.ShellConfig) *extension.BuiltinTool {
	return &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "shell",
			Description: "Execute a shell command on the host system. Only available to authorized users. Returns command output.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The command to execute, e.g. 'uname -a' or 'ls -la'",
					},
				},
				"required": []any{"command"},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			if !cfg.Enabled {
				return &extension.ExtResponse{Error: "shell tool is not enabled"}, nil
			}

			// Check user authorization
			if !isUserAllowed(req.UserID, cfg.AllowedUsers) {
				return &extension.ExtResponse{Error: "not authorized to use shell"}, nil
			}

			var args shellArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			if args.Command == "" {
				return &extension.ExtResponse{Error: "command is required"}, nil
			}

			// Check command whitelist
			if !isCommandAllowed(args.Command, cfg.AllowedCommands) {
				return &extension.ExtResponse{Error: fmt.Sprintf("command not allowed: %s", args.Command)}, nil
			}

			parts, err := shlex.Split(args.Command)
			if err != nil {
				return &extension.ExtResponse{Error: "invalid shell syntax: " + err.Error()}, nil
			}
			if len(parts) == 0 {
				return &extension.ExtResponse{Error: "empty command"}, nil
			}

			// Execute
			timeout := cfg.TimeoutDuration()
			ctx, cancel := context.WithTimeout(ctx, timeout)
			defer cancel()

			cmd := exec.CommandContext(ctx, parts[0], parts[1:]...)

			var stdout, stderr bytes.Buffer
			cmd.Stdout = &stdout
			cmd.Stderr = &stderr

			if err := cmd.Run(); err != nil {
				return &extension.ExtResponse{
					Output: fmt.Sprintf("Error: %s\nStderr: %s", err, stderr.String()),
				}, nil
			}

			output := stdout.String()
			if len(output) > 4000 {
				output = output[:4000] + "\n... (truncated)"
			}

			return &extension.ExtResponse{Output: output}, nil
		},
	}
}

func isUserAllowed(userID string, allowedUsers []string) bool {
	if len(allowedUsers) == 0 {
		return false // empty list = nobody allowed
	}
	for _, id := range allowedUsers {
		if id == userID {
			return true
		}
	}
	return false
}

func isCommandAllowed(command string, allowedCommands []string) bool {
	if len(allowedCommands) == 0 {
		return true // empty list = all commands allowed
	}
	parts, err := shlex.Split(command)
	if err != nil || len(parts) == 0 {
		return false
	}
	base := parts[0]
	for _, allowed := range allowedCommands {
		if base == allowed {
			return true
		}
	}
	return false
}
