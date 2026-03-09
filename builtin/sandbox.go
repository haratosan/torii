package builtin

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync"
	"time"

	"github.com/haratosan/torii/config"
	"github.com/haratosan/torii/extension"
)

const containerName = "torii-sandbox"

type sandboxManager struct {
	cfg      *config.SandboxConfig
	mu       sync.Mutex
	lastUsed time.Time
	running  bool
	stopCh   chan struct{}
	logger   *slog.Logger
}

type sandboxArgs struct {
	Command string `json:"command"`
}

func NewSandboxTool(cfg *config.SandboxConfig, logger *slog.Logger) (*extension.BuiltinTool, *sandboxManager) {
	mgr := &sandboxManager{
		cfg:    cfg,
		stopCh: make(chan struct{}),
		logger: logger,
	}

	go mgr.idleWatcher()

	tool := &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "sandbox",
			Description: "Execute shell commands in an isolated Linux container. Use this to install packages (apk add), run scripts, compile code, and create files. Each chat has its own working directory. Files persist across commands.",
			Parameters: map[string]any{
				"type": "object",
				"properties": map[string]any{
					"command": map[string]any{
						"type":        "string",
						"description": "The shell command to execute in the container",
					},
				},
				"required": []any{"command"},
			},
		},
		Handler: func(ctx context.Context, req extension.ExtRequest) (*extension.ExtResponse, error) {
			if !cfg.Enabled {
				return &extension.ExtResponse{Error: "sandbox tool is not enabled"}, nil
			}

			var args sandboxArgs
			if err := json.Unmarshal([]byte(req.Input), &args); err != nil {
				return &extension.ExtResponse{Error: "invalid arguments: " + err.Error()}, nil
			}

			if args.Command == "" {
				return &extension.ExtResponse{Error: "command is required"}, nil
			}

			output, err := mgr.execute(ctx, args.Command, req.ChatID)
			if err != nil {
				return &extension.ExtResponse{Error: err.Error()}, nil
			}

			return &extension.ExtResponse{Output: output}, nil
		},
	}

	return tool, mgr
}

func (m *sandboxManager) resolveSharedDir() string {
	dir := m.cfg.SharedDir
	if len(dir) > 0 && dir[0] == '~' {
		if home, err := os.UserHomeDir(); err == nil {
			dir = filepath.Join(home, dir[1:])
		}
	}
	return dir
}

func (m *sandboxManager) ensureRunning() error {
	// Check if container is already running
	out, err := exec.Command("container", "inspect", containerName).CombinedOutput()
	if err == nil && strings.Contains(string(out), containerName) {
		m.running = true
		return nil
	}

	// Remove stale container if it exists
	exec.Command("container", "rm", "-f", containerName).Run()

	sharedDir := m.resolveSharedDir()
	if err := os.MkdirAll(sharedDir, 0755); err != nil {
		return fmt.Errorf("create shared dir: %w", err)
	}

	m.logger.Info("starting sandbox container", "image", m.cfg.Image, "shared_dir", sharedDir)

	cmd := exec.Command("container",
		"run", "-d",
		"--name", containerName,
		"-v", sharedDir+":/shared",
		m.cfg.Image,
		"sleep", "infinity",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("start container: %s (%w)", stderr.String(), err)
	}

	m.running = true
	m.logger.Info("sandbox container started")
	return nil
}

func (m *sandboxManager) execute(ctx context.Context, command, chatID string) (string, error) {
	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureRunning(); err != nil {
		return "", err
	}

	m.lastUsed = time.Now()

	// Ensure chat workdir exists inside the container
	workdir := "/shared"
	if chatID != "" {
		workdir = "/shared/" + chatID
		exec.CommandContext(ctx, "container", "exec", containerName, "mkdir", "-p", workdir).Run()
	}

	cmd := exec.CommandContext(ctx, "container", "exec", "-w", workdir, containerName, "sh", "-c", command)
	var stdout, stderr bytes.Buffer
	cmd.Stdout = &stdout
	cmd.Stderr = &stderr

	err := cmd.Run()

	output := stdout.String()
	if errOut := stderr.String(); errOut != "" {
		if output != "" {
			output += "\n"
		}
		output += errOut
	}

	if err != nil {
		if output == "" {
			output = err.Error()
		} else {
			output += "\n(exit: " + err.Error() + ")"
		}
	}

	if len(output) > 4000 {
		output = output[:4000] + "\n... (truncated)"
	}

	return output, nil
}

func (m *sandboxManager) idleWatcher() {
	ticker := time.NewTicker(1 * time.Minute)
	defer ticker.Stop()

	for {
		select {
		case <-ticker.C:
			m.mu.Lock()
			if m.running && !m.lastUsed.IsZero() && time.Since(m.lastUsed) > m.cfg.IdleTimeoutDuration() {
				m.logger.Info("stopping idle sandbox container")
				exec.Command("container", "stop", containerName).Run()
				exec.Command("container", "rm", containerName).Run()
				m.running = false
			}
			m.mu.Unlock()
		case <-m.stopCh:
			return
		}
	}
}

func (m *sandboxManager) Shutdown() {
	close(m.stopCh)
	m.mu.Lock()
	defer m.mu.Unlock()

	if m.running {
		m.logger.Info("shutting down sandbox container")
		exec.Command("container", "stop", containerName).Run()
		exec.Command("container", "rm", containerName).Run()
		m.running = false
	}
}
