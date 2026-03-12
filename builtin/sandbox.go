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
	"runtime"
	"strings"
	"sync"
	"time"

	"github.com/haratosan/torii/config"
	"github.com/haratosan/torii/extension"
)

const containerPrefix = "torii-sandbox-"

type containerState struct {
	running  bool
	lastUsed time.Time
}

type sandboxManager struct {
	cfg          *config.SandboxConfig
	containerBin string // absolute path to container CLI
	mu           sync.Mutex
	containers   map[string]*containerState // chatID → state
	stopCh       chan struct{}
	logger       *slog.Logger
}

type sandboxArgs struct {
	Command string `json:"command"`
}

// lookupContainerBin finds the container CLI binary.
// On Linux it looks for docker; on macOS it looks for the Apple container CLI.
func lookupContainerBin() (string, error) {
	if runtime.GOOS == "linux" {
		if p, err := exec.LookPath("docker"); err == nil {
			return p, nil
		}
		if _, err := os.Stat("/usr/bin/docker"); err == nil {
			return "/usr/bin/docker", nil
		}
		return "", fmt.Errorf("docker not found (install Docker: https://docs.docker.com/engine/install/)")
	}

	// macOS: use Apple container CLI
	if p, err := exec.LookPath("container"); err == nil {
		return p, nil
	}
	for _, p := range []string{"/opt/homebrew/bin/container", "/usr/local/bin/container"} {
		if _, err := os.Stat(p); err == nil {
			return p, nil
		}
	}
	return "", fmt.Errorf("container CLI not found (install with: brew install container)")
}

func NewSandboxTool(cfg *config.SandboxConfig, logger *slog.Logger) (*extension.BuiltinTool, *sandboxManager) {
	bin, err := lookupContainerBin()
	if err != nil {
		logger.Warn("sandbox: container CLI not found, tool will fail at runtime", "error", err)
		bin = "container" // fallback, will produce a clear error
	} else {
		logger.Info("sandbox: found container CLI", "path", bin)
	}

	mgr := &sandboxManager{
		cfg:          cfg,
		containerBin: bin,
		containers:   make(map[string]*containerState),
		stopCh:       make(chan struct{}),
		logger:       logger,
	}

	go mgr.idleWatcher()

	tool := &extension.BuiltinTool{
		Def: extension.Manifest{
			Name:        "sandbox",
			Description: "Execute shell commands in an isolated Linux container. Use this to install packages (apk add), run scripts, compile code, and create files. Each chat has its own isolated container with a persistent /workspace directory.",
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

func (m *sandboxManager) containerName(chatID string) string {
	return containerPrefix + chatID
}

func (m *sandboxManager) ensureRunning(chatID string) error {
	name := m.containerName(chatID)

	// Check if we already know it's running
	if cs, ok := m.containers[chatID]; ok && cs.running {
		// Verify it's actually still running
		out, err := exec.Command(m.containerBin, "inspect", name).CombinedOutput()
		if err == nil && strings.Contains(string(out), name) {
			return nil
		}
		cs.running = false
	}

	// Check if container exists but we don't know about it
	out, err := exec.Command(m.containerBin, "inspect", name).CombinedOutput()
	if err == nil && strings.Contains(string(out), name) {
		if m.containers[chatID] == nil {
			m.containers[chatID] = &containerState{}
		}
		m.containers[chatID].running = true
		return nil
	}

	// Remove stale container if it exists
	exec.Command(m.containerBin, "rm", "-f", name).Run()

	// Create host directory for this chat
	chatDir := filepath.Join(m.resolveSharedDir(), chatID)
	if err := os.MkdirAll(chatDir, 0755); err != nil {
		return fmt.Errorf("create chat dir: %w", err)
	}

	m.logger.Info("starting sandbox container", "name", name, "image", m.cfg.Image, "chat_dir", chatDir)

	cmd := exec.Command(m.containerBin,
		"run", "-d",
		"--name", name,
		"-v", chatDir+":/workspace",
		m.cfg.Image,
		"sleep", "infinity",
	)
	var stderr bytes.Buffer
	cmd.Stderr = &stderr

	if err := cmd.Run(); err != nil {
		return fmt.Errorf("start container: %s (%w)", stderr.String(), err)
	}

	m.containers[chatID] = &containerState{running: true}
	m.logger.Info("sandbox container started", "name", name)
	return nil
}

func (m *sandboxManager) execute(ctx context.Context, command, chatID string) (string, error) {
	if chatID == "" {
		return "", fmt.Errorf("chatID is required for sandbox execution")
	}

	m.mu.Lock()
	defer m.mu.Unlock()

	if err := m.ensureRunning(chatID); err != nil {
		return "", err
	}

	m.containers[chatID].lastUsed = time.Now()

	name := m.containerName(chatID)
	cmd := exec.CommandContext(ctx, m.containerBin, "exec", "-w", "/workspace", name, "sh", "-c", command)
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
			timeout := m.cfg.IdleTimeoutDuration()
			for chatID, cs := range m.containers {
				if cs.running && !cs.lastUsed.IsZero() && time.Since(cs.lastUsed) > timeout {
					name := m.containerName(chatID)
					m.logger.Info("stopping idle sandbox container", "name", name)
					exec.Command(m.containerBin, "stop", name).Run()
					exec.Command(m.containerBin, "rm", name).Run()
					cs.running = false
				}
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

	for chatID, cs := range m.containers {
		if cs.running {
			name := m.containerName(chatID)
			m.logger.Info("shutting down sandbox container", "name", name)
			exec.Command(m.containerBin, "stop", name).Run()
			exec.Command(m.containerBin, "rm", name).Run()
			cs.running = false
		}
	}
}
