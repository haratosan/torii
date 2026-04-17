package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/haratosan/torii/agent"
	"github.com/haratosan/torii/builtin"
	"github.com/haratosan/torii/channel"
	"github.com/haratosan/torii/cmd"
	"github.com/haratosan/torii/config"
	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/gateway"
	"github.com/haratosan/torii/knowledge"
	"github.com/haratosan/torii/llm"
	"github.com/haratosan/torii/mcp"
	"github.com/haratosan/torii/pdf"
	"github.com/haratosan/torii/scheduler"
	"github.com/haratosan/torii/session"
	"github.com/haratosan/torii/store"
)

func main() {
	// Handle service subcommands
	if len(os.Args) > 1 {
		switch os.Args[1] {
		case "start":
			cmd.Start()
			return
		case "stop":
			cmd.Stop()
			return
		case "restart":
			cmd.Restart()
			return
		case "status":
			cmd.Status()
			return
		case "logs":
			cmd.Logs()
			return
		default:
			cmd.Usage()
		}
	}

	// Load config: try CWD first, then ~/.config/torii/
	configPath := "config.yaml"
	if _, err := os.Stat(configPath); os.IsNotExist(err) {
		if home, err := os.UserHomeDir(); err == nil {
			candidate := home + "/.config/torii/config.yaml"
			if _, err := os.Stat(candidate); err == nil {
				configPath = candidate
			}
		}
	}
	cfg, err := config.Load(configPath)
	if err != nil {
		fmt.Fprintf(os.Stderr, "config error: %v\n", err)
		os.Exit(1)
	}

	// Setup logger
	var logLevel slog.Level
	switch cfg.Gateway.LogLevel {
	case "debug":
		logLevel = slog.LevelDebug
	case "warn":
		logLevel = slog.LevelWarn
	case "error":
		logLevel = slog.LevelError
	default:
		logLevel = slog.LevelInfo
	}
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: logLevel}))

	// Setup SQLite store
	db, err := store.New(cfg.Scheduler.DBPath)
	if err != nil {
		logger.Error("database error", "error", err)
		os.Exit(1)
	}
	defer db.Close()
	logger.Info("database opened", "path", cfg.Scheduler.DBPath)

	if docs, chunks, chats, err := db.CountKBDocuments(); err == nil {
		logger.Info("kb stats", "documents", docs, "chunks", chunks, "chats", chats)
	}

	// Setup LLM provider
	var provider llm.Provider
	switch cfg.LLM.Provider {
	case "ollama":
		ollamaProvider, ollamaErr := llm.NewOllama(cfg.LLM.Ollama.Host, cfg.LLM.Ollama.Model, logger)
		if ollamaErr != nil {
			logger.Error("ollama provider error", "error", ollamaErr)
			os.Exit(1)
		}
		if vm := cfg.Knowledge.VisionModel; vm != "" {
			ollamaProvider.SetVisionModel(vm)
			logger.Info("using ollama", "host", cfg.LLM.Ollama.Host, "model", cfg.LLM.Ollama.Model, "vision_model", vm)
		} else {
			logger.Info("using ollama", "host", cfg.LLM.Ollama.Host, "model", cfg.LLM.Ollama.Model)
		}
		provider = ollamaProvider
	case "openrouter":
		if cfg.LLM.OpenRouter.APIKey == "" {
			logger.Error("openrouter api key required (set TORII_OPENROUTER_API_KEY or config.yaml)")
			os.Exit(1)
		}
		provider = llm.NewOpenRouter(cfg.LLM.OpenRouter.APIKey, cfg.LLM.OpenRouter.Model, logger)
		logger.Info("using openrouter", "model", cfg.LLM.OpenRouter.Model)
	default:
		logger.Error("unknown llm provider", "provider", cfg.LLM.Provider)
		os.Exit(1)
	}

	// Setup extensions
	registry := extension.NewRegistry(logger)
	if err := registry.Discover(cfg.Extensions.Dirs); err != nil {
		logger.Error("extension discovery error", "error", err)
		os.Exit(1)
	}

	// Register built-in tools
	registry.RegisterBuiltin(builtin.NewMemoryTool(db))
	registry.RegisterBuiltin(builtin.NewBotProfileTool(db))
	registry.RegisterBuiltin(builtin.NewShellTool(&cfg.Shell))
	registry.RegisterBuiltin(builtin.NewRemindTool(db))
	registry.RegisterBuiltin(builtin.NewCronTool(db))
	registry.RegisterBuiltin(builtin.NewNoReplyTool())
	registry.RegisterBuiltin(builtin.NewButtonsTool())

	var ks *knowledge.KnowledgeStore
	if cfg.Knowledge.Enabled {
		ollamaHost := cfg.LLM.Ollama.Host
		if ollamaHost == "" {
			ollamaHost = "http://localhost:11434"
		}
		embedder := knowledge.NewOllamaEmbedder(ollamaHost, cfg.Knowledge.EmbeddingModel)
		ks = knowledge.NewKnowledgeStore(db, embedder, cfg.Knowledge.ChunkSize, cfg.Knowledge.ChunkOverlap)
		registry.RegisterBuiltin(builtin.NewKnowledgeTool(ks))
		logger.Info("knowledge base enabled", "model", cfg.Knowledge.EmbeddingModel)

		// Warn if the stored embedding dimension doesn't match what the
		// current model produces — this happens after switching
		// embedding_model without running `knowledge reembed`.
		go func() {
			dbDim, err := db.SampleKBChunkDimension()
			if err != nil || dbDim == 0 {
				return
			}
			ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
			defer cancel()
			probe, err := embedder.Embed(ctx, "dimension probe")
			if err != nil || len(probe) == 0 {
				return
			}
			if len(probe) != dbDim {
				logger.Warn("embedding dimension mismatch — existing chunks are unusable, run `knowledge reembed`",
					"db_dim", dbDim,
					"model_dim", len(probe),
					"model", cfg.Knowledge.EmbeddingModel,
				)
			}
		}()
	}

	if cfg.Sandbox.Enabled {
		sandboxTool, sandboxMgr := builtin.NewSandboxTool(&cfg.Sandbox, logger)
		registry.RegisterBuiltin(sandboxTool)
		defer sandboxMgr.Shutdown()
	}

	executor := extension.NewExecutor(registry, cfg.Extensions.TimeoutDuration(), cfg.Extensions.Env, logger)

	// Setup session store (DB-backed for persistence across restarts)
	sessions := session.NewStore(cfg.Session.MaxHistory, db, logger)

	// Setup agent
	// Determine model name based on provider
	var modelName string
	switch cfg.LLM.Provider {
	case "ollama":
		modelName = cfg.LLM.Ollama.Model
	case "openrouter":
		modelName = cfg.LLM.OpenRouter.Model
	}

	ag := agent.New(provider, executor, registry, sessions, db, cfg.Gateway.SystemPrompt, cfg.Gateway.MaxToolRounds, &cfg.Onboarding, cfg.LLM.Provider, modelName, logger)

	// Setup MCP servers
	if len(cfg.MCP.Servers) > 0 {
		mcpConfigs := make([]mcp.ServerConfig, len(cfg.MCP.Servers))
		for i, s := range cfg.MCP.Servers {
			mcpConfigs[i] = mcp.ServerConfig{
				Name:      s.Name,
				Transport: s.Transport,
				Command:   s.Command,
				Args:      s.Args,
				URL:       s.URL,
			}
		}
		mcpManager := mcp.NewManager(logger)
		mcpManager.Start(context.Background(), mcpConfigs)
		executor.SetMCPManager(mcpManager)
		ag.SetMCPToolProvider(mcpManager)
		defer mcpManager.Shutdown()
		logger.Info("mcp servers configured", "count", len(cfg.MCP.Servers))
	}

	// Setup Telegram channel
	if cfg.Telegram.Token == "" {
		logger.Error("telegram token required (set TORII_TELEGRAM_TOKEN or config.yaml)")
		os.Exit(1)
	}

	var transcriber channel.TranscribeFn
	if _, err := registry.Get("transcribe"); err == nil {
		transcriber = func(ctx context.Context, filePath string) (string, error) {
			input, _ := json.Marshal(map[string]string{"file_path": filePath})
			resp, err := executor.Execute(ctx, "transcribe", string(input), "", "", nil)
			if err != nil {
				return "", err
			}
			if resp.Error != "" {
				return "", errors.New(resp.Error)
			}
			return resp.Output, nil
		}
		logger.Info("voice transcription enabled")
	}

	ch := channel.NewTelegram(cfg.Telegram.Token, cfg.Telegram.AllowedUsers, transcriber, logger)

	// Setup PDF import function
	var pdfImportFn gateway.PDFImportFn
	if ks != nil && cfg.Knowledge.VisionModel != "" {
		ollamaHost := cfg.LLM.Ollama.Host
		if ollamaHost == "" {
			ollamaHost = "http://localhost:11434"
		}
		visionModel := cfg.Knowledge.VisionModel
		maxPages := cfg.Knowledge.MaxPDFPages
		pdfImportFn = func(ctx context.Context, chatID, fileName string, data []byte) (string, error) {
			pages, err := pdf.ToImages(data, maxPages)
			if err != nil {
				return "", fmt.Errorf("convert pdf: %w", err)
			}

			text, err := pdf.ExtractText(ctx, ollamaHost, visionModel, pages)
			if err != nil {
				return "", fmt.Errorf("extract text: %w", err)
			}

			title := strings.TrimSuffix(fileName, ".pdf")
			title = strings.TrimSuffix(title, ".PDF")
			docID, err := ks.Add(ctx, chatID, title, text)
			if err != nil {
				return "", fmt.Errorf("store document: %w", err)
			}

			return fmt.Sprintf("PDF '%s' imported (ID: %d, %d pages, %d chars).", title, docID, len(pages), len(text)), nil
		}
		logger.Info("pdf import enabled", "vision_model", visionModel, "max_pages", maxPages)
	}

	// Setup gateway
	gw := gateway.New(ch, ag, cfg.Gateway.AgentTimeoutDuration(), cfg.Extensions.Dirs, pdfImportFn, logger)

	// Run with graceful shutdown
	ctx, cancel := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer cancel()

	// Start scheduler in background
	sched := scheduler.New(db, ch, ag, sessions, cfg.Scheduler.IntervalDuration(), logger)
	go sched.Run(ctx)

	if err := gw.Run(ctx); err != nil {
		logger.Error("gateway error", "error", err)
		os.Exit(1)
	}

	logger.Info("torii stopped")
}
