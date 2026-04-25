package extension

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// MCPExecutor is the interface the executor uses to call MCP tools.
type MCPExecutor interface {
	HasTool(name string) bool
	Execute(ctx context.Context, toolName string, input string) (*ExtResponse, error)
}

type Executor struct {
	registry   *Registry
	timeout    time.Duration
	envMap     map[string]string
	mcpManager MCPExecutor
	logger     *slog.Logger
}

func NewExecutor(registry *Registry, timeout time.Duration, envMap map[string]string, logger *slog.Logger) *Executor {
	return &Executor{
		registry: registry,
		timeout:  timeout,
		envMap:   envMap,
		logger:   logger,
	}
}

// SetMCPManager sets the MCP manager for tool execution fallback.
func (e *Executor) SetMCPManager(m MCPExecutor) {
	e.mcpManager = m
}

func (e *Executor) Execute(ctx context.Context, name string, input string, chatID string, userID string, images [][]byte) (*ExtResponse, error) {
	// Self-evolution chokepoint: decrement quotas / reject forbidden writes
	// before any handler runs. This applies to builtins, extensions, and MCP.
	if lim, ok := EvolutionLimitsFromContext(ctx); ok {
		if msg := evoGate(name, input, lim); msg != "" {
			e.logger.Info("evolution gate denied call", "name", name, "reason", msg)
			return &ExtResponse{Error: msg}, nil
		}
	}

	// Check builtins first
	if bt, ok := e.registry.GetBuiltin(name); ok {
		req := ExtRequest{
			Action: name,
			Input:  input,
			ChatID: chatID,
			UserID: userID,
		}
		e.logger.Info("executing builtin", "name", name, "input", input)
		return bt.Handler(ctx, req)
	}

	ext, err := e.registry.Get(name)
	if err != nil {
		// Fallback to MCP
		if e.mcpManager != nil && e.mcpManager.HasTool(name) {
			e.logger.Info("executing mcp tool", "name", name, "input", input)
			return e.mcpManager.Execute(ctx, name, input)
		}
		return nil, err
	}

	if ext.Manifest.Type == "command" {
		return e.executeCommand(ctx, ext, input)
	}

	var b64Images []string
	for _, img := range images {
		b64Images = append(b64Images, base64.StdEncoding.EncodeToString(img))
	}

	req := ExtRequest{
		Action: name,
		Input:  input,
		ChatID: chatID,
		UserID: userID,
		Images: b64Images,
	}

	reqJSON, err := json.Marshal(req)
	if err != nil {
		return nil, fmt.Errorf("marshal request: %w", err)
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	cmd := exec.CommandContext(ctx, ext.Executable)
	cmd.Dir = ext.Dir
	cmd.Stdin = bytes.NewReader(reqJSON)

	// Env isolation: start with empty env, only pass declared vars
	// Config env values first, then system env overrides
	cmd.Env = []string{}
	for _, key := range ext.Manifest.Env {
		if val, ok := e.envMap[key]; ok {
			cmd.Env = append(cmd.Env, key+"="+val)
		}
		if val, ok := os.LookupEnv(key); ok {
			cmd.Env = append(cmd.Env, key+"="+val)
		}
	}

	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	e.logger.Info("executing extension", "name", name, "input", input)

	if err := cmd.Run(); err != nil {
		e.logger.Error("extension failed", "name", name, "error", err, "stderr", stderr.String())
		return &ExtResponse{
			Error: fmt.Sprintf("extension %s failed: %s", name, stderr.String()),
		}, nil
	}

	var resp ExtResponse
	if err := json.Unmarshal(stdout.Bytes(), &resp); err != nil {
		return nil, fmt.Errorf("unmarshal response from %s: %w (raw: %s)", name, err, stdout.String())
	}

	if resp.Error != "" {
		e.logger.Warn("extension returned error", "name", name, "error", resp.Error)
	}

	return &resp, nil
}

// evoGate enforces self-evolution quotas. Returns "" if the call is allowed,
// otherwise an error message that the caller surfaces as ExtResponse.Error.
// The match is action-aware so unrelated actions (skills list/get,
// memory get/list) pass through untouched.
func evoGate(name, input string, lim *EvolutionLimits) string {
	var a struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal([]byte(input), &a)
	switch name {
	case "skills":
		switch a.Action {
		case "add":
			if lim.addsLeft.Add(-1) < 0 {
				return "evolution: skills add cap reached for this run"
			}
		case "update":
			if lim.updatesLeft.Add(-1) < 0 {
				return "evolution: skills update cap reached for this run"
			}
		case "remove":
			return "evolution: skills remove forbidden in self-evolution mode"
		}
	case "memory":
		switch a.Action {
		case "add", "replace", "remove", "set", "delete":
			return "evolution: memory writes forbidden — read-only mode"
		}
	}
	return ""
}

func (e *Executor) executeCommand(ctx context.Context, ext *Extension, input string) (*ExtResponse, error) {
	var params map[string]string
	if err := json.Unmarshal([]byte(input), &params); err != nil {
		return nil, fmt.Errorf("parse command input: %w", err)
	}

	cmd := ext.Manifest.Command
	for key, val := range params {
		cmd = strings.ReplaceAll(cmd, "{{"+key+"}}", val)
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	proc := exec.CommandContext(ctx, "sh", "-c", cmd)
	proc.Dir = ext.Dir
	proc.Env = os.Environ()

	var stdout, stderr bytes.Buffer
	proc.Stdout = &stdout
	proc.Stderr = &stderr

	e.logger.Info("executing command extension", "name", ext.Manifest.Name, "command", cmd)

	if err := proc.Run(); err != nil {
		return &ExtResponse{
			Error: fmt.Sprintf("command failed: %s (stderr: %s)", err, stderr.String()),
		}, nil
	}

	return &ExtResponse{
		Output: strings.TrimSpace(stdout.String()),
	}, nil
}
