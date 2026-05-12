package api

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"

	"github.com/haratosan/torii/channel"
	"github.com/haratosan/torii/extension"
	"github.com/haratosan/torii/llm"
	"github.com/haratosan/torii/session"
	"github.com/haratosan/torii/store"
)

// alwaysAllowedAPITools are tools that pass through the policy gate
// regardless of the per-user allowlist. They are agent-internal scaffolding:
// no-reply suppresses output, and a tool that the API user can't call but
// also can't see would just produce confusing errors.
var alwaysAllowedAPITools = []string{"no-reply"}

// maxChatCompletionsBody bounds the request body we will accept on the
// OpenAI-compatible endpoint. The server is normally bound to 127.0.0.1
// behind a reverse proxy, but enforcing a cap here means a misbehaving (or
// hostile) client cannot drive the bot OOM with a multi-GB payload.
const maxChatCompletionsBody = 1 << 20 // 1 MiB

// handleModels returns a single-entry models list. OpenWebUI uses this to
// populate its model picker; multiple variants (e.g. torii-fast, torii-full)
// would be added here in a later phase.
func (s *Server) handleModels(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodGet {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	resp := ModelsResponse{
		Object: "list",
		Data: []Model{{
			ID:      s.cfg.ModelLabel,
			Object:  "model",
			Created: time.Now().Unix(),
			OwnedBy: "torii",
		}},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// handleChatCompletions accepts an OpenAI-shaped request, runs torii's
// agent loop with an ephemeral session, and returns either a final response
// or a synthesized SSE stream.
func (s *Server) handleChatCompletions(w http.ResponseWriter, r *http.Request) {
	if r.Method != http.MethodPost {
		writeJSONError(w, http.StatusMethodNotAllowed, "method not allowed")
		return
	}
	apiUser, ok := apiUserFromContext(r.Context())
	if !ok {
		writeJSONError(w, http.StatusUnauthorized, "no api user in context")
		return
	}

	r.Body = http.MaxBytesReader(w, r.Body, maxChatCompletionsBody)
	bodyBytes, err := io.ReadAll(r.Body)
	if err != nil {
		// MaxBytesReader surfaces "http: request body too large" as a normal
		// read error; bucket both that and real I/O failures into 413 so the
		// caller can tell payload-size from transport problems.
		writeJSONError(w, http.StatusRequestEntityTooLarge, "request body rejected: "+err.Error())
		return
	}
	r.Body = io.NopCloser(bytes.NewReader(bodyBytes)) // restore so subsequent reads work

	var req ChatCompletionRequest
	if err := json.Unmarshal(bodyBytes, &req); err != nil {
		// Body is not logged: it may contain user secrets pasted into the
		// chat. The size + user is enough to triage a malformed client.
		s.logger.Info("api: bad JSON body", "user", apiUser.Name, "bytes", len(bodyBytes))
		writeJSONError(w, http.StatusBadRequest, "invalid JSON body: "+err.Error())
		return
	}
	if len(req.Messages) == 0 {
		s.logger.Info("api: empty messages array", "user", apiUser.Name, "bytes", len(bodyBytes))
		writeJSONError(w, http.StatusBadRequest, "messages array is required and non-empty")
		return
	}

	// Pull off the trailing user message — that's what HandleMessage expects
	// as the "new" turn. Any messages BEFORE it are loaded into the
	// ephemeral session as history. If the request ends with an assistant
	// message (continuation request, rare), use the last user message we
	// can find; everything after it is preserved as history but won't be
	// re-replied to.
	userText, history := splitTrailingUser(req.Messages)
	if userText == "" {
		writeJSONError(w, http.StatusBadRequest, "no user message found in messages array")
		return
	}

	// Identity: linked Telegram user shares memory/skills; otherwise
	// namespaced under api:<id> so the agent's per-user lookups hit a
	// separate scope without polluting the Telegram user's data.
	userID := userIDForAPIUser(apiUser)
	requestID := fmt.Sprintf("chatcmpl-%d", time.Now().UnixNano())
	chatID := fmt.Sprintf("api:%d:%s", apiUser.ID, requestID)

	// Build per-request tool policy. Default deny: only granted tools pass.
	granted, err := s.db.GetAPIUserTools(apiUser.ID)
	if err != nil {
		s.logger.Error("api: load tool grants", "error", err, "user_id", apiUser.ID)
		writeJSONError(w, http.StatusInternalServerError, "failed to load tool permissions")
		return
	}
	policy := &extension.APIToolPolicy{Allowed: map[string]bool{}}
	for _, t := range alwaysAllowedAPITools {
		policy.Allowed[t] = true
	}
	for _, t := range granted {
		policy.Allowed[t] = true
	}

	// Ephemeral session: nil DB means session.Append is purely in-memory,
	// so /v1/chat/completions calls never touch session_messages. Skills
	// and memory still hit the real DB through the agent's `store` field —
	// only conversation history is ephemeral.
	ephemeral := session.NewStore(64, nil, s.logger)
	for _, m := range history {
		ephemeral.Append(chatID, userID, llm.ChatMessage{
			Role:    llm.Role(m.Role),
			Content: m.Content,
		})
	}

	agent := s.agent.WithSessions(ephemeral)
	ctx := extension.WithAPIToolPolicy(r.Context(), policy)
	ctx, cancel := context.WithTimeout(ctx, s.requestTimeout)
	defer cancel()

	result, err := agent.HandleMessage(ctx, channel.Message{
		ChatID: chatID,
		UserID: userID,
		Text:   userText,
	})
	if err != nil {
		s.logger.Error("api: agent run", "error", err, "user", apiUser.Name)
		writeJSONError(w, http.StatusInternalServerError, "agent error: "+err.Error())
		return
	}

	final := result.Text
	if result.Silent {
		// no-reply was called — return an empty assistant message. OpenAI
		// clients tolerate empty content; OpenWebUI shows nothing, which
		// matches the Telegram silence semantic.
		final = ""
	}

	if req.Stream {
		if err := streamPseudoChunks(w, requestID, s.cfg.ModelLabel, final); err != nil {
			s.logger.Error("api: stream", "error", err)
		}
		return
	}

	resp := ChatCompletionResponse{
		ID:      requestID,
		Object:  "chat.completion",
		Created: time.Now().Unix(),
		Model:   s.cfg.ModelLabel,
		Choices: []ChatCompletionChoice{{
			Index:        0,
			Message:      ChatMessage{Role: "assistant", Content: final},
			FinishReason: "stop",
		}},
		Usage: ChatCompletionUsage{},
	}
	w.Header().Set("Content-Type", "application/json")
	_ = json.NewEncoder(w).Encode(resp)
}

// splitTrailingUser picks the last user message as the new turn and returns
// everything before it as history. Non-user messages after the last user
// message are dropped (continuation-request edge case).
func splitTrailingUser(msgs []ChatMessage) (text string, history []ChatMessage) {
	for i := len(msgs) - 1; i >= 0; i-- {
		if msgs[i].Role == "user" {
			return strings.TrimSpace(msgs[i].Content), msgs[:i]
		}
	}
	return "", nil
}

// userIDForAPIUser returns the user_id string the agent should see for skill
// and memory scoping. Linked Telegram users share state; unlinked ones get
// an isolated `api:<id>` namespace.
func userIDForAPIUser(u *store.APIUser) string {
	if u.LinkedTelegramUserID != "" {
		return u.LinkedTelegramUserID
	}
	return fmt.Sprintf("api:%d", u.ID)
}

