package session

import (
	"encoding/json"
	"log/slog"
	"sync"

	"github.com/haratosan/torii/llm"
	"github.com/haratosan/torii/store"
)

type Session struct {
	Messages []llm.ChatMessage
	loaded   bool
}

type Store struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	maxHistory int
	db         *store.Store
	logger     *slog.Logger
}

func NewStore(maxHistory int, db *store.Store, logger *slog.Logger) *Store {
	return &Store{
		sessions:   make(map[string]*Session),
		maxHistory: maxHistory,
		db:         db,
		logger:     logger,
	}
}

func (s *Store) Get(chatID string) *Session {
	s.mu.RLock()
	sess, ok := s.sessions[chatID]
	s.mu.RUnlock()
	if ok && sess.loaded {
		return sess
	}

	s.mu.Lock()
	defer s.mu.Unlock()

	// Double-check
	if sess, ok = s.sessions[chatID]; ok && sess.loaded {
		return sess
	}

	sess = &Session{loaded: true}

	// Load from DB
	if s.db != nil {
		dbMsgs, err := s.db.LoadMessages(chatID, s.maxHistory)
		if err != nil {
			s.logger.Error("failed to load session from db", "chat_id", chatID, "error", err)
		} else if len(dbMsgs) > 0 {
			for _, m := range dbMsgs {
				msg := llm.ChatMessage{
					Role:       llm.Role(m.Role),
					Content:    m.Content,
					ToolCallID: m.ToolCallID,
				}
				if m.ToolCalls != "" {
					var tcs []llm.ToolCall
					if err := json.Unmarshal([]byte(m.ToolCalls), &tcs); err == nil {
						msg.ToolCalls = tcs
					}
				}
				sess.Messages = append(sess.Messages, msg)
			}
			s.logger.Debug("session loaded from db", "chat_id", chatID, "messages", len(sess.Messages))
		}
	}

	s.sessions[chatID] = sess
	return sess
}

func (s *Store) Append(chatID string, msgs ...llm.ChatMessage) {
	sess := s.Get(chatID)
	s.mu.Lock()
	defer s.mu.Unlock()

	sess.Messages = append(sess.Messages, msgs...)

	// Persist to DB
	if s.db != nil {
		for _, msg := range msgs {
			var toolCallsJSON string
			if len(msg.ToolCalls) > 0 {
				if b, err := json.Marshal(msg.ToolCalls); err == nil {
					toolCallsJSON = string(b)
				}
			}
			if err := s.db.SaveMessage(chatID, string(msg.Role), msg.Content, toolCallsJSON, msg.ToolCallID); err != nil {
				s.logger.Error("failed to persist message", "chat_id", chatID, "error", err)
			}
		}
	}

	// Trim to max history
	if len(sess.Messages) > s.maxHistory {
		sess.Messages = sess.Messages[len(sess.Messages)-s.maxHistory:]
		// Trim DB too
		if s.db != nil {
			if err := s.db.TrimMessages(chatID, s.maxHistory); err != nil {
				s.logger.Error("failed to trim db messages", "chat_id", chatID, "error", err)
			}
		}
	}
}

func (s *Store) Clear(chatID string) {
	s.mu.Lock()
	defer s.mu.Unlock()
	delete(s.sessions, chatID)

	if s.db != nil {
		if err := s.db.DeleteMessages(chatID); err != nil {
			s.logger.Error("failed to delete session from db", "chat_id", chatID, "error", err)
		}
	}
}

func (s *Store) MaxHistory() int {
	return s.maxHistory
}

func (s *Store) History(chatID string) []llm.ChatMessage {
	sess := s.Get(chatID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]llm.ChatMessage, len(sess.Messages))
	copy(result, sess.Messages)
	return result
}
