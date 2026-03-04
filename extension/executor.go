package extension

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"time"
)

type Executor struct {
	registry *Registry
	timeout  time.Duration
	logger   *slog.Logger
}

func NewExecutor(registry *Registry, timeout time.Duration, logger *slog.Logger) *Executor {
	return &Executor{
		registry: registry,
		timeout:  timeout,
		logger:   logger,
	}
}

func (e *Executor) Execute(ctx context.Context, name string, input string, chatID string, userID string) (*ExtResponse, error) {
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
		return nil, err
	}

	req := ExtRequest{
		Action: name,
		Input:  input,
		ChatID: chatID,
		UserID: userID,
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
	cmd.Env = []string{}
	for _, key := range ext.Manifest.Env {
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
