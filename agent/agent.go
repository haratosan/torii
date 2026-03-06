package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"

	"github.com/haratosan/torii/channel"
	"github.com/haratosan/torii/config"
	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/llm"
	"github.com/haratosan/torii/session"
	"github.com/haratosan/torii/store"
)

type Agent struct {
	provider      llm.Provider
	executor      *extension.Executor
	registry      *extension.Registry
	sessions      *session.Store
	store         *store.Store
	systemPrompt  string
	maxToolRounds int
	onboarding    *config.OnboardingConfig
	logger        *slog.Logger
}

func New(provider llm.Provider, executor *extension.Executor, registry *extension.Registry, sessions *session.Store, db *store.Store, systemPrompt string, maxToolRounds int, onboarding *config.OnboardingConfig, logger *slog.Logger) *Agent {
	return &Agent{
		provider:      provider,
		executor:      executor,
		registry:      registry,
		sessions:      sessions,
		store:         db,
		systemPrompt:  systemPrompt,
		maxToolRounds: maxToolRounds,
		onboarding:    onboarding,
		logger:        logger,
	}
}

// AgentResponse contains the text reply and optional metadata from tool execution.
type AgentResponse struct {
	Text      string
	ImagePath string
}

func (a *Agent) HandleMessage(ctx context.Context, msg channel.Message) (*AgentResponse, error) {
	// Append user message to history
	a.sessions.Append(msg.ChatID, llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: msg.Text,
		Images:  msg.Images,
	})

	// Build tool definitions from registry
	tools := a.buildToolDefs()

	// Keep user images for passing to tool executions
	userImages := msg.Images

	var lastImagePath string

	// Agent loop: LLM may request tool calls multiple times
	for round := 0; round < a.maxToolRounds; round++ {
		messages := a.buildMessages(msg.ChatID, msg.UserID)

		resp, err := a.provider.Chat(ctx, llm.ChatRequest{
			Messages: messages,
			Tools:    tools,
		})
		if err != nil {
			return nil, fmt.Errorf("llm chat: %w", err)
		}

		// No tool calls → final answer
		if len(resp.ToolCalls) == 0 {
			a.sessions.Append(msg.ChatID, llm.ChatMessage{
				Role:    llm.RoleAssistant,
				Content: resp.Content,
			})
			return &AgentResponse{Text: resp.Content, ImagePath: lastImagePath}, nil
		}

		// Store assistant message with tool calls
		a.sessions.Append(msg.ChatID, llm.ChatMessage{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call
		for _, tc := range resp.ToolCalls {
			a.logger.Info("tool call", "name", tc.Function.Name, "args", tc.Function.Arguments)

			result, err := a.executor.Execute(ctx, tc.Function.Name, tc.Function.Arguments, msg.ChatID, msg.UserID, userImages)
			if err != nil {
				a.logger.Error("tool execution failed", "name", tc.Function.Name, "error", err)
				a.sessions.Append(msg.ChatID, llm.ChatMessage{
					Role:       llm.RoleTool,
					Content:    fmt.Sprintf("Error: %s", err),
					ToolCallID: tc.ID,
				})
				continue
			}

			output := result.Output
			if result.Error != "" {
				output = fmt.Sprintf("Error: %s", result.Error)
			}

			// Check for image_path in tool result data
			if result.Data != nil {
				if imgPath, ok := result.Data["image_path"].(string); ok && imgPath != "" {
					lastImagePath = imgPath
				}
			}

			a.sessions.Append(msg.ChatID, llm.ChatMessage{
				Role:       llm.RoleTool,
				Content:    output,
				ToolCallID: tc.ID,
			})
		}
	}

	return &AgentResponse{Text: "I've reached the maximum number of tool calls. Here's what I found so far.", ImagePath: lastImagePath}, nil
}

func (a *Agent) buildMessages(chatID string, userID string) []llm.ChatMessage {
	prompt := a.buildSystemPrompt(userID)
	messages := []llm.ChatMessage{
		{Role: llm.RoleSystem, Content: prompt},
	}
	messages = append(messages, a.sessions.History(chatID)...)
	return messages
}

func (a *Agent) buildSystemPrompt(userID string) string {
	var sb strings.Builder

	// Bot profile from DB (overrides config system_prompt)
	profile, _ := a.store.GetAllBotProfile()
	if name := profile["name"]; name != "" {
		sb.WriteString(fmt.Sprintf("Your name is %s.\n", name))
	}
	if personality := profile["personality"]; personality != "" {
		sb.WriteString(personality)
		sb.WriteString("\n")
	}
	if sysPrompt := profile["system_prompt"]; sysPrompt != "" {
		sb.WriteString(sysPrompt)
		sb.WriteString("\n")
	} else {
		sb.WriteString(a.systemPrompt)
		sb.WriteString("\n")
	}

	// User memory
	notes, _ := a.store.GetMemory(userID)
	if notes != "" {
		sb.WriteString(fmt.Sprintf("\nUser notes: %s\n", notes))
	}

	// Onboarding for new users
	if a.onboarding != nil && a.onboarding.Enabled && notes == "" {
		hasMemory, _ := a.store.HasMemory(userID)
		if !hasMemory {
			sb.WriteString("\nThis is a new user. Ask them the following to get to know them:\n")
			for _, q := range a.onboarding.Questions {
				sb.WriteString(fmt.Sprintf("- %s\n", q))
			}
			sb.WriteString("After they answer, save their information using the memory tool.\n")
		}
	}

	return sb.String()
}

func (a *Agent) buildToolDefs() []llm.ToolDef {
	exts := a.registry.List()
	builtins := a.registry.ListBuiltins()
	tools := make([]llm.ToolDef, 0, len(exts)+len(builtins))

	for _, ext := range exts {
		params := ext.Manifest.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, llm.ToolDef{
			Name:        ext.Manifest.Name,
			Description: ext.Manifest.Description,
			Parameters:  params,
		})
	}

	for _, bt := range builtins {
		params := bt.Def.Parameters
		if params == nil {
			params = map[string]any{"type": "object", "properties": map[string]any{}}
		}
		tools = append(tools, llm.ToolDef{
			Name:        bt.Def.Name,
			Description: bt.Def.Description,
			Parameters:  params,
		})
	}

	return tools
}

// ToolDefsJSON returns tool definitions as JSON for debugging.
func (a *Agent) ToolDefsJSON() string {
	tools := a.buildToolDefs()
	b, _ := json.MarshalIndent(tools, "", "  ")
	return string(b)
}
