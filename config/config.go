package config

import (
	"os"
	"time"

	"gopkg.in/yaml.v3"
)

type Config struct {
	Gateway    GatewayConfig    `yaml:"gateway"`
	Telegram   TelegramConfig   `yaml:"telegram"`
	LLM        LLMConfig        `yaml:"llm"`
	Extensions ExtensionsConfig `yaml:"extensions"`
	Session    SessionConfig    `yaml:"session"`
	Scheduler  SchedulerConfig  `yaml:"scheduler"`
	Shell      ShellConfig      `yaml:"shell"`
	Sandbox    SandboxConfig    `yaml:"sandbox"`
	Onboarding OnboardingConfig `yaml:"onboarding"`
	MCP        MCPConfig        `yaml:"mcp"`
}

type MCPConfig struct {
	Servers []MCPServerConfig `yaml:"servers"`
}

type MCPServerConfig struct {
	Name      string   `yaml:"name"`
	Transport string   `yaml:"transport"` // "stdio" or "sse"
	Command   string   `yaml:"command"`   // for stdio
	Args      []string `yaml:"args"`      // for stdio
	URL       string   `yaml:"url"`       // for sse
}

type GatewayConfig struct {
	LogLevel      string `yaml:"log_level"`
	SystemPrompt  string `yaml:"system_prompt"`
	MaxToolRounds int    `yaml:"max_tool_rounds"`
	AgentTimeout  string `yaml:"agent_timeout"`
}

func (c *GatewayConfig) AgentTimeoutDuration() time.Duration {
	if c.AgentTimeout == "" || c.AgentTimeout == "0" {
		return 0
	}
	d, err := time.ParseDuration(c.AgentTimeout)
	if err != nil {
		return 0
	}
	return d
}

type TelegramConfig struct {
	Token        string  `yaml:"token"`
	AllowedUsers []int64 `yaml:"allowed_users"`
}

type LLMConfig struct {
	Provider   string           `yaml:"provider"`
	Ollama     OllamaConfig     `yaml:"ollama"`
	OpenRouter OpenRouterConfig `yaml:"openrouter"`
}

type OllamaConfig struct {
	Host  string `yaml:"host"`
	Model string `yaml:"model"`
}

type OpenRouterConfig struct {
	APIKey string `yaml:"api_key"`
	Model  string `yaml:"model"`
}

type ExtensionsConfig struct {
	Dirs    []string          `yaml:"dirs"`
	Timeout string            `yaml:"timeout"`
	Env     map[string]string `yaml:"env"`
}

type SessionConfig struct {
	MaxHistory int `yaml:"max_history"`
}

type SchedulerConfig struct {
	DBPath   string `yaml:"db_path"`
	Interval string `yaml:"interval"`
}

func (c *SchedulerConfig) IntervalDuration() time.Duration {
	d, err := time.ParseDuration(c.Interval)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

type ShellConfig struct {
	Enabled         bool     `yaml:"enabled"`
	AllowedUsers    []string `yaml:"allowed_users"`
	AllowedCommands []string `yaml:"allowed_commands"`
	Timeout         string   `yaml:"timeout"`
}

func (c *ShellConfig) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 10 * time.Second
	}
	return d
}

type SandboxConfig struct {
	Enabled     bool   `yaml:"enabled"`
	Image       string `yaml:"image"`
	SharedDir   string `yaml:"shared_dir"`
	IdleTimeout string `yaml:"idle_timeout"`
}

func (c *SandboxConfig) IdleTimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.IdleTimeout)
	if err != nil {
		return 10 * time.Minute
	}
	return d
}

type OnboardingConfig struct {
	Enabled   bool     `yaml:"enabled"`
	Questions []string `yaml:"questions"`
}

func (c *ExtensionsConfig) TimeoutDuration() time.Duration {
	d, err := time.ParseDuration(c.Timeout)
	if err != nil {
		return 30 * time.Second
	}
	return d
}

func Load(path string) (*Config, error) {
	cfg := &Config{
		Gateway: GatewayConfig{
			LogLevel:      "info",
			SystemPrompt: `You are Torii, a personal AI assistant running on Telegram.

Key behaviors:
- Respond in the user's language.
- Keep responses concise and natural — this is a chat, not an essay.
- Proactively use the memory tool to remember important information about the user (name, preferences, context from past conversations).
- When uncertain, ask clarifying questions rather than guessing.

Sandbox:
- You have a persistent Linux sandbox (Alpine container) where you can execute shell commands freely.
- Your working directory is always /workspace — all files you create persist there across commands.
- You can install any packages you need with ` + "`apk add`" + ` (e.g. python3, nodejs, gcc, curl, git).
- Use the sandbox whenever the user asks you to run code, process data, create files, download things, or do anything that benefits from actual command execution. Do not hesitate to use it.

Scheduling:
- You can set one-shot reminders and recurring tasks (cron jobs) for the user.`,
			MaxToolRounds: 10,
			AgentTimeout:  "5m",
		},
		LLM: LLMConfig{
			Provider: "ollama",
			Ollama: OllamaConfig{
				Host:  "http://localhost:11434",
				Model: "llama3.2",
			},
			OpenRouter: OpenRouterConfig{
				Model: "anthropic/claude-sonnet-4",
			},
		},
		Extensions: ExtensionsConfig{
			Dirs:    []string{"./extensions", "~/.local/share/torii/extensions"},
			Timeout: "30s",
		},
		Session: SessionConfig{
			MaxHistory: 50,
		},
		Scheduler: SchedulerConfig{
			DBPath:   "torii.db",
			Interval: "30s",
		},
		Shell: ShellConfig{
			Enabled: false,
			Timeout: "10s",
		},
		Sandbox: SandboxConfig{
			Enabled:     false,
			Image:       "alpine:latest",
			SharedDir:   "~/torii-sandbox",
			IdleTimeout: "10m",
		},
		Onboarding: OnboardingConfig{
			Enabled: false,
		},
	}

	data, err := os.ReadFile(path)
	if err != nil && !os.IsNotExist(err) {
		return nil, err
	}
	if err == nil {
		if err := yaml.Unmarshal(data, cfg); err != nil {
			return nil, err
		}
	}

	// Env vars override YAML
	if v := os.Getenv("TORII_TELEGRAM_TOKEN"); v != "" {
		cfg.Telegram.Token = v
	}
	if v := os.Getenv("TORII_OPENROUTER_API_KEY"); v != "" {
		cfg.LLM.OpenRouter.APIKey = v
	}
	if v := os.Getenv("TORII_LLM_PROVIDER"); v != "" {
		cfg.LLM.Provider = v
	}
	if v := os.Getenv("TORII_OLLAMA_HOST"); v != "" {
		cfg.LLM.Ollama.Host = v
	}
	if v := os.Getenv("TORII_OLLAMA_MODEL"); v != "" {
		cfg.LLM.Ollama.Model = v
	}
	if v := os.Getenv("TORII_OPENROUTER_MODEL"); v != "" {
		cfg.LLM.OpenRouter.Model = v
	}
	if v := os.Getenv("TORII_LOG_LEVEL"); v != "" {
		cfg.Gateway.LogLevel = v
	}

	return cfg, nil
}
