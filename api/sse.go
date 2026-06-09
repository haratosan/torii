package api

import (
	"encoding/json"
	"fmt"
	"net/http"
	"sync"
	"time"
)

// streamPseudoChunks splits a fully-resolved assistant text into ~chunkSize
// fragments and emits them as OpenAI-shaped SSE events. We don't currently
// stream end-to-end (Ollama Chat is non-streaming through the existing
// llm.Provider interface) — but OpenWebUI degrades visibly without
// `stream: true`, so we synthesize the stream client-side. UX is identical
// for short replies; longer replies appear in 50-char chunks with no real
// pacing benefit, but the typing-indicator stays alive.
func streamPseudoChunks(w http.ResponseWriter, id, model, text string) error {
	sw, err := newSSEWriter(w)
	if err != nil {
		return err
	}
	if err := sw.writeBegin(id, model); err != nil {
		return err
	}
	if err := sw.writeContent(id, model, text); err != nil {
		return err
	}
	return sw.writeEnd(id, model)
}

// sseWriter is the staged SSE encoder used by both the plain pseudo-stream
// path and the verbose-mode handler (which interleaves tool_call/tool_result
// vendor frames between the role chunk and the content chunks).
type sseWriter struct {
	w       http.ResponseWriter
	flusher http.Flusher
	mu      sync.Mutex
	created int64
}

func newSSEWriter(w http.ResponseWriter) (*sseWriter, error) {
	flusher, ok := w.(http.Flusher)
	if !ok {
		return nil, fmt.Errorf("response writer does not support flushing")
	}
	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no")
	return &sseWriter{w: w, flusher: flusher, created: time.Now().Unix()}, nil
}

// writeBegin emits the role:assistant opening chunk (per OpenAI spec, the
// first chunk announces the role; subsequent chunks carry deltas).
func (s *sseWriter) writeBegin(id, model string) error {
	return s.writeChunk(ChatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: s.created,
		Model:   model,
		Choices: []ChatCompletionChunkChoice{{
			Index: 0,
			Delta: ChatCompletionDelta{Role: "assistant"},
		}},
	})
}

// writeContent emits the body of the response as 50-char content deltas.
func (s *sseWriter) writeContent(id, model, text string) error {
	const chunkSize = 50
	for i := 0; i < len(text); i += chunkSize {
		end := i + chunkSize
		if end > len(text) {
			end = len(text)
		}
		safe := utf8SafeSlice(text, i, end)
		if safe == "" {
			continue
		}
		if err := s.writeChunk(ChatCompletionChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: s.created,
			Model:   model,
			Choices: []ChatCompletionChunkChoice{{
				Index: 0,
				Delta: ChatCompletionDelta{Content: safe},
			}},
		}); err != nil {
			return err
		}
	}
	return nil
}

// writeEnd emits the final stop-marker chunk plus the SSE [DONE] sentinel.
func (s *sseWriter) writeEnd(id, model string) error {
	stop := "stop"
	if err := s.writeChunk(ChatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: s.created,
		Model:   model,
		Choices: []ChatCompletionChunkChoice{{
			Index:        0,
			Delta:        ChatCompletionDelta{},
			FinishReason: &stop,
		}},
	}); err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprint(s.w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// writeVendorEvent emits an arbitrary JSON payload as a single SSE data frame.
// Used by verbose mode to interleave tool_call/tool_result events between the
// role and content chunks. OpenAI-typed clients ignore frames whose JSON
// doesn't match the chunk schema (they look for choices[]); the TUI client
// recognises the `torii_event` discriminator and renders it as a tool-status
// line.
func (s *sseWriter) writeVendorEvent(payload any) error {
	b, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

func (s *sseWriter) writeChunk(c ChatCompletionChunk) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, err := fmt.Fprintf(s.w, "data: %s\n\n", b); err != nil {
		return err
	}
	s.flusher.Flush()
	return nil
}

// utf8SafeSlice trims forward/backward to the nearest valid UTF-8 boundary
// so a chunk never splits a multi-byte rune. The byte-window we get from
// chunk slicing may straddle codepoints; we bias toward the original window
// but step inward when needed.
func utf8SafeSlice(s string, start, end int) string {
	if start >= len(s) {
		return ""
	}
	// Step start forward to a rune boundary.
	for start < end && start > 0 && (s[start]&0xC0) == 0x80 {
		start++
	}
	// Step end backward to a rune boundary.
	for end > start && end < len(s) && (s[end]&0xC0) == 0x80 {
		end--
	}
	return s[start:end]
}
