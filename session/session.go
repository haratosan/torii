package session

import (
	"sync"

	"github.com/haratosan/torii/llm"
)

type Session struct {
	Messages []llm.ChatMessage
}

type Store struct {
	mu         sync.RWMutex
	sessions   map[string]*Session
	maxHistory int
}

func NewStore(maxHistory int) *Store {
	return &Store{
		sessions:   make(map[string]*Session),
		maxHistory: maxHistory,
	}
}

func (s *Store) Get(chatID string) *Session {
	s.mu.RLock()
	sess, ok := s.sessions[chatID]
	s.mu.RUnlock()
	if ok {
		return sess
	}

	s.mu.Lock()
	defer s.mu.Unlock()
	// Double-check
	if sess, ok = s.sessions[chatID]; ok {
		return sess
	}
	sess = &Session{}
	s.sessions[chatID] = sess
	return sess
}

func (s *Store) Append(chatID string, msgs ...llm.ChatMessage) {
	sess := s.Get(chatID)
	s.mu.Lock()
	defer s.mu.Unlock()
	sess.Messages = append(sess.Messages, msgs...)
	// Trim to max history
	if len(sess.Messages) > s.maxHistory {
		sess.Messages = sess.Messages[len(sess.Messages)-s.maxHistory:]
	}
}

func (s *Store) History(chatID string) []llm.ChatMessage {
	sess := s.Get(chatID)
	s.mu.RLock()
	defer s.mu.RUnlock()
	result := make([]llm.ChatMessage, len(sess.Messages))
	copy(result, sess.Messages)
	return result
}
