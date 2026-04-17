package agent

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/haratosan/torii/channel"
	"github.com/haratosan/torii/config"
	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/llm"
	"github.com/haratosan/torii/session"
	"github.com/haratosan/torii/store"
)

// MCPToolProvider provides MCP tool definitions for the agent.
type MCPToolProvider interface {
	Tools() []llm.ToolDef
}

type Agent struct {
	provider      llm.Provider
	executor      *extension.Executor
	registry      *extension.Registry
	sessions      *session.Store
	store         *store.Store
	systemPrompt  string
	maxToolRounds int
	onboarding    *config.OnboardingConfig
	providerName  string
	modelName     string
	mcpTools      MCPToolProvider
	logger        *slog.Logger
}

func New(provider llm.Provider, executor *extension.Executor, registry *extension.Registry, sessions *session.Store, db *store.Store, systemPrompt string, maxToolRounds int, onboarding *config.OnboardingConfig, providerName string, modelName string, logger *slog.Logger) *Agent {
	return &Agent{
		provider:      provider,
		executor:      executor,
		registry:      registry,
		sessions:      sessions,
		store:         db,
		systemPrompt:  systemPrompt,
		maxToolRounds: maxToolRounds,
		onboarding:    onboarding,
		providerName:  providerName,
		modelName:     modelName,
		logger:        logger,
	}
}

// SetMCPToolProvider sets the MCP tool provider for tool discovery.
func (a *Agent) SetMCPToolProvider(p MCPToolProvider) {
	a.mcpTools = p
}

// AgentResponse contains the text reply and optional metadata from tool execution.
type AgentResponse struct {
	Text      string
	ImagePath string
	Silent    bool
	Buttons   [][]channel.Button
}

func (a *Agent) HandleCommand(msg channel.Message) (string, bool) {
	if !strings.HasPrefix(msg.Text, "/") {
		return "", false
	}
	cmd := strings.TrimSpace(msg.Text)
	switch cmd {
	case "/new":
		a.sessions.Clear(msg.ChatID)
		return "Session cleared.", true
	case "/status":
		count := len(a.sessions.History(msg.ChatID))
		text := fmt.Sprintf("Messages in session: %d/%d\nProvider: %s\nModel: %s",
			count, a.sessions.MaxHistory(), a.providerName, a.modelName)
		return text, true
	case "/system":
		prompt := a.buildSystemPrompt(msg.UserID)
		return prompt, true
	case "/help":
		return "/new — Start new session\n/status — Show bot info\n/system — Show system prompt\n/help — Show commands", true
	}
	return "", false
}

func (a *Agent) HandleMessage(ctx context.Context, msg channel.Message) (*AgentResponse, error) {
	// Build user content, including reply context if present
	content := msg.Text
	if msg.ReplyText != "" {
		content = "[Replying to: " + msg.ReplyText + "]\n\n" + content
	}

	// Append user message to history
	a.sessions.Append(msg.ChatID, llm.ChatMessage{
		Role:    llm.RoleUser,
		Content: content,
		Images:  msg.Images,
	})

	// Determine the chat ID for tool execution (may differ from session chat ID for cron tasks)
	toolChatID := msg.ChatID
	if msg.ToolChatID != "" {
		toolChatID = msg.ToolChatID
	}

	// Build tool definitions from registry
	tools := a.buildToolDefs()

	// Keep user images for passing to tool executions
	userImages := msg.Images

	var lastImagePath string
	var lastButtons [][]channel.Button
	var silent bool

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

		// No tool calls → final answer (or retry if empty)
		if len(resp.ToolCalls) == 0 {
			text := stripModelArtifacts(resp.Content)
			if text == "" {
				a.logger.Warn("empty response from model, retrying", "round", round)
				continue
			}
			a.sessions.Append(msg.ChatID, llm.ChatMessage{
				Role:    llm.RoleAssistant,
				Content: text,
			})
			return &AgentResponse{Text: text, ImagePath: lastImagePath, Silent: silent, Buttons: lastButtons}, nil
		}

		// Store assistant message with tool calls
		a.sessions.Append(msg.ChatID, llm.ChatMessage{
			Role:      llm.RoleAssistant,
			Content:   resp.Content,
			ToolCalls: resp.ToolCalls,
		})

		// Execute each tool call
		for _, tc := range resp.ToolCalls {
			if tc.Function.Name == "no-reply" {
				silent = true
			}

			if tc.Function.Name == "" {
				a.logger.Warn("skipping tool call with empty name", "args", tc.Function.Arguments)
				a.sessions.Append(msg.ChatID, llm.ChatMessage{
					Role:       llm.RoleTool,
					Content:    "Error: tool call had an empty function name. Please retry with the correct function name.",
					ToolCallID: tc.ID,
				})
				continue
			}

			a.logger.Info("tool call", "name", tc.Function.Name, "args", tc.Function.Arguments)

			result, err := a.executor.Execute(ctx, tc.Function.Name, tc.Function.Arguments, toolChatID, msg.UserID, userImages)
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

			// Log tool result (truncated)
			logOutput := output
			if len(logOutput) > 200 {
				logOutput = logOutput[:200] + "..."
			}
			a.logger.Info("tool result", "name", tc.Function.Name, "output", logOutput)

			// Check for image_path and buttons in tool result data
			if result.Data != nil {
				if imgPath, ok := result.Data["image_path"].(string); ok && imgPath != "" {
					lastImagePath = imgPath
				}
				if btns, ok := result.Data["buttons"]; ok {
					lastButtons = parseButtons(btns)
				}
			}

			a.sessions.Append(msg.ChatID, llm.ChatMessage{
				Role:       llm.RoleTool,
				Content:    output,
				ToolCallID: tc.ID,
			})
		}

		// If no-reply was called, stop the agent loop immediately
		if silent {
			return &AgentResponse{Silent: true, ImagePath: lastImagePath, Buttons: lastButtons}, nil
		}
	}

	return &AgentResponse{Text: "I've reached the maximum number of tool calls. Here's what I found so far.", ImagePath: lastImagePath, Buttons: lastButtons}, nil
}

// parseButtons converts the generic button data from ExtResponse.Data into typed Button slices.
func parseButtons(data any) [][]channel.Button {
	rows, ok := data.([][]map[string]string)
	if ok {
		var result [][]channel.Button
		for _, row := range rows {
			var btnRow []channel.Button
			for _, btn := range row {
				btnRow = append(btnRow, channel.Button{
					Text:  btn["text"],
					Value: btn["value"],
				})
			}
			result = append(result, btnRow)
		}
		return result
	}

	// Handle interface{} slices (from JSON unmarshaling)
	rowsAny, ok := data.([]any)
	if !ok {
		return nil
	}
	var result [][]channel.Button
	for _, rowAny := range rowsAny {
		rowSlice, ok := rowAny.([]any)
		if !ok {
			continue
		}
		var btnRow []channel.Button
		for _, btnAny := range rowSlice {
			btnMap, ok := btnAny.(map[string]string)
			if ok {
				btnRow = append(btnRow, channel.Button{Text: btnMap["text"], Value: btnMap["value"]})
				continue
			}
			btnMapAny, ok := btnAny.(map[string]any)
			if ok {
				text, _ := btnMapAny["text"].(string)
				value, _ := btnMapAny["value"].(string)
				btnRow = append(btnRow, channel.Button{Text: text, Value: value})
			}
		}
		result = append(result, btnRow)
	}
	return result
}

func (a *Agent) buildMessages(chatID string, userID string) []llm.ChatMessage {
	prompt := a.buildSystemPrompt(userID)
	messages := []llm.ChatMessage{
		{Role: llm.RoleSystem, Content: prompt},
	}
	history := a.sessions.History(chatID)
	// Strip images from all messages except the most recent user message.
	// Historical images are huge, waste tokens, and cause repeated vision-
	// fallback triggers when the main model doesn't support images.
	lastUserIdx := -1
	for i := len(history) - 1; i >= 0; i-- {
		if history[i].Role == llm.RoleUser && len(history[i].Images) > 0 {
			lastUserIdx = i
			break
		}
	}
	for i, m := range history {
		if i != lastUserIdx && len(m.Images) > 0 {
			cleaned := m
			cleaned.Images = nil
			messages = append(messages, cleaned)
		} else {
			messages = append(messages, m)
		}
	}
	return messages
}

func (a *Agent) buildSystemPrompt(userID string) string {
	var sb strings.Builder

	// Config system prompt is always the base
	sb.WriteString(a.systemPrompt)
	sb.WriteString("\n")

	// Bot profile from DB (extends the base prompt)
	profile, _ := a.store.GetAllBotProfile()
	if name := profile["name"]; name != "" {
		sb.WriteString(fmt.Sprintf("\nYour name is %s.\n", name))
	}
	if personality := profile["personality"]; personality != "" {
		sb.WriteString("\n")
		sb.WriteString(personality)
		sb.WriteString("\n")
	}
	if sysPrompt := profile["system_prompt"]; sysPrompt != "" {
		sb.WriteString("\n")
		sb.WriteString(sysPrompt)
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

	now := time.Now()
	sb.WriteString(fmt.Sprintf(
		"\nCurrent date and time: %s (%s, %s)\n",
		now.Format("2006-01-02 15:04:05"),
		now.Weekday().String(),
		now.Format("MST"),
	))

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

	// Append MCP tools (lowest priority — builtins > extensions > MCP)
	if a.mcpTools != nil {
		// Build set of existing tool names to avoid collisions
		existing := make(map[string]bool, len(tools))
		for _, t := range tools {
			existing[t.Name] = true
		}
		for _, t := range a.mcpTools.Tools() {
			if existing[t.Name] {
				a.logger.Debug("mcp tool shadowed by existing tool", "name", t.Name)
				continue
			}
			tools = append(tools, t)
		}
	}

	return tools
}

// stripModelArtifacts removes internal markers that reasoning models leak
// into their output: <think> blocks, tool-call section markers, etc.
var (
	thinkRe    = regexp.MustCompile(`(?s)<think>.*?</think>\s*`)
	toolTagsRe = regexp.MustCompile(`(?s)<\|tool_calls_section_begin\|>.*?<\|tool_calls_section_end\|>\s*`)
)

func stripModelArtifacts(s string) string {
	s = thinkRe.ReplaceAllString(s, "")
	s = toolTagsRe.ReplaceAllString(s, "")
	return strings.TrimSpace(s)
}

// ToolDefsJSON returns tool definitions as JSON for debugging.
func (a *Agent) ToolDefsJSON() string {
	tools := a.buildToolDefs()
	b, _ := json.MarshalIndent(tools, "", "  ")
	return string(b)
}
