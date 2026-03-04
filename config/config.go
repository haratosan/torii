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
	Onboarding OnboardingConfig `yaml:"onboarding"`
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
	Dirs    []string `yaml:"dirs"`
	Timeout string   `yaml:"timeout"`
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
			SystemPrompt:  "You are Torii, a helpful AI assistant.",
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
			Dirs:    []string{"./extensions"},
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
