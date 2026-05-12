package extension

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"os"
	"os/exec"
	"strings"
	"time"
)

// maxExtensionOutputBytes caps the stdout/stderr we will buffer from any
// extension or command run, so a runaway tool can't OOM the bot.
const maxExtensionOutputBytes = 4 * 1024 * 1024

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

	// Cron chokepoint: when the run was triggered by the scheduler (and not
	// by a live user), refuse high-blast tools. Cron task descriptions are
	// LLM-controlled at creation time and replayed verbatim — without this
	// gate, a single successful prompt injection could persist as a daily
	// shell/memory-write job.
	if _, isCron := CronExecutionFromContext(ctx); isCron {
		if msg := cronGate(name, input); msg != "" {
			e.logger.Info("cron gate denied call", "name", name, "reason", msg)
			return &ExtResponse{Error: msg}, nil
		}
	}

	// API caller chokepoint: per-user tool allowlist (default deny). Only
	// applied when the request originated from the HTTP API; Telegram-driven
	// calls have no policy in ctx and pass through.
	if pol, ok := APIToolPolicyFromContext(ctx); ok {
		if !pol.Allowed[name] {
			e.logger.Info("api policy denied tool call", "name", name, "user", userID)
			return &ExtResponse{
				Error: fmt.Sprintf("tool '%s' not permitted for this API user", name),
			}, nil
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
		e.logger.Info("executing builtin", "name", name, "input", truncateLogValue(input))
		return bt.Handler(ctx, req)
	}

	ext, err := e.registry.Get(name)
	if err != nil {
		// Fallback to MCP
		if e.mcpManager != nil && e.mcpManager.HasTool(name) {
			e.logger.Info("executing mcp tool", "name", name, "input", truncateLogValue(input))
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

	stdout := newLimitedBuffer(maxExtensionOutputBytes)
	stderr := newLimitedBuffer(maxExtensionOutputBytes)
	cmd.Stdout = stdout
	cmd.Stderr = stderr

	e.logger.Info("executing extension", "name", name, "input", truncateLogValue(input))

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

// cronGate denies high-blast tool calls during a scheduler-triggered agent
// run. Read-only actions (memory get/list, skills list/get) pass through so
// the cron payload can still consult state and craft a normal reply.
func cronGate(name, input string) string {
	switch name {
	case "shell", "sandbox":
		return "cron: '" + name + "' tool not callable from a scheduled task — refuse and reply with text only"
	}
	var a struct {
		Action string `json:"action"`
	}
	_ = json.Unmarshal([]byte(input), &a)
	switch name {
	case "memory":
		switch a.Action {
		case "add", "replace", "remove", "set", "delete":
			return "cron: memory writes forbidden from scheduled tasks"
		}
	case "skills":
		switch a.Action {
		case "add", "update", "remove":
			return "cron: skills writes forbidden from scheduled tasks"
		}
	}
	return ""
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

	argv, err := buildArgv(ext.Manifest, params)
	if err != nil {
		return nil, err
	}
	if len(argv) == 0 {
		return nil, fmt.Errorf("extension %q has neither argv nor command", ext.Manifest.Name)
	}

	ctx, cancel := context.WithTimeout(ctx, e.timeout)
	defer cancel()

	proc := exec.CommandContext(ctx, argv[0], argv[1:]...)
	proc.Dir = ext.Dir
	// Env isolation: same allowlist semantics as binary extensions. The legacy
	// `os.Environ()` leak (including secrets like TORII_TELEGRAM_TOKEN) is
	// gone — only vars declared in the manifest's `env` field pass through.
	proc.Env = buildCommandEnv(ext.Manifest.Env, e.envMap)

	stdout := newLimitedBuffer(maxExtensionOutputBytes)
	stderr := newLimitedBuffer(maxExtensionOutputBytes)
	proc.Stdout = stdout
	proc.Stderr = stderr

	e.logger.Info("executing command extension", "name", ext.Manifest.Name, "argv0", argv[0])

	if err := proc.Run(); err != nil {
		return &ExtResponse{
			Error: fmt.Sprintf("command failed: %s (stderr: %s)", err, stderr.String()),
		}, nil
	}

	return &ExtResponse{
		Output: strings.TrimSpace(stdout.String()),
	}, nil
}

// buildArgv produces the exec argv for a command-type extension.
//   - Preferred path: manifest.Argv is set. Each element gets {{param}}
//     placeholders replaced; each element stays a separate argv slot, so a
//     user-controlled value cannot become a shell metacharacter.
//   - Legacy path: only manifest.Command is set. We render with shellQuote
//     applied to each value and dispatch via `sh -c`. This preserves old
//     extensions but at least closes the shell-injection hole.
func buildArgv(m Manifest, params map[string]string) ([]string, error) {
	if len(m.Argv) > 0 {
		out := make([]string, 0, len(m.Argv))
		for _, a := range m.Argv {
			for key, val := range params {
				a = strings.ReplaceAll(a, "{{"+key+"}}", val)
			}
			out = append(out, a)
		}
		return out, nil
	}
	if m.Command != "" {
		cmd := m.Command
		for key, val := range params {
			cmd = strings.ReplaceAll(cmd, "{{"+key+"}}", shellQuote(val))
		}
		return []string{"sh", "-c", cmd}, nil
	}
	return nil, nil
}

// shellQuote wraps s in single quotes for safe use inside a POSIX sh -c
// string. Single quotes inside the value are escaped via the classic
// 'closing-quote, escaped quote, reopening-quote' dance.
func shellQuote(s string) string {
	return "'" + strings.ReplaceAll(s, "'", `'\''`) + "'"
}

// buildCommandEnv mirrors the binary-extension env-allowlist policy: start
// from an empty environment, then append values for each declared key — first
// from the configured envMap, then from the process environment (which wins
// on duplicate, because exec uses the last occurrence).
func buildCommandEnv(allow []string, envMap map[string]string) []string {
	env := []string{}
	for _, key := range allow {
		if val, ok := envMap[key]; ok {
			env = append(env, key+"="+val)
		}
		if val, ok := os.LookupEnv(key); ok {
			env = append(env, key+"="+val)
		}
	}
	return env
}

// limitedBuffer is a bytes.Buffer that silently drops writes once the cap is
// reached. Used to cap captured stdout/stderr from extension processes so a
// rogue extension can't OOM the bot.
type limitedBuffer struct {
	buf bytes.Buffer
	cap int
}

func newLimitedBuffer(capBytes int) *limitedBuffer {
	return &limitedBuffer{cap: capBytes}
}

func (l *limitedBuffer) Write(p []byte) (int, error) {
	remaining := l.cap - l.buf.Len()
	if remaining <= 0 {
		// Pretend we accepted everything so the child process isn't killed
		// with a broken pipe — we just stop persisting bytes.
		return len(p), nil
	}
	if len(p) > remaining {
		l.buf.Write(p[:remaining])
		return len(p), nil
	}
	return l.buf.Write(p)
}

func (l *limitedBuffer) Bytes() []byte  { return l.buf.Bytes() }
func (l *limitedBuffer) String() string { return l.buf.String() }
func (l *limitedBuffer) Len() int       { return l.buf.Len() }

// Ensure the *limitedBuffer satisfies io.Writer.
var _ io.Writer = (*limitedBuffer)(nil)

// truncateLogValue trims long strings used as structured-log values so we
// don't dump 200KB of LLM-generated JSON (which may include user secrets) on
// every tool call. The cap is generous enough that normal tool args still
// log in full.
func truncateLogValue(s string) string {
	const max = 512
	if len(s) <= max {
		return s
	}
	return s[:max] + "…(truncated)"
}
