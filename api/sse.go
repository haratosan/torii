package api

import (
	"encoding/json"
	"fmt"
	"net/http"
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
	flusher, ok := w.(http.Flusher)
	if !ok {
		return fmt.Errorf("response writer does not support flushing")
	}

	w.Header().Set("Content-Type", "text/event-stream")
	w.Header().Set("Cache-Control", "no-cache")
	w.Header().Set("Connection", "keep-alive")
	w.Header().Set("X-Accel-Buffering", "no") // for any nginx reverse-proxy in front

	created := time.Now().Unix()

	// First chunk announces the role; subsequent chunks carry only content
	// deltas (per OpenAI spec).
	if err := writeChunk(w, ChatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []ChatCompletionChunkChoice{{
			Index: 0,
			Delta: ChatCompletionDelta{Role: "assistant"},
		}},
	}); err != nil {
		return err
	}
	flusher.Flush()

	const chunkSize = 50
	for i := 0; i < len(text); i += chunkSize {
		end := i + chunkSize
		if end > len(text) {
			end = len(text)
		}
		// Slice on rune boundaries so we don't split UTF-8 mid-codepoint.
		safe := utf8SafeSlice(text, i, end)
		if safe == "" {
			continue
		}
		if err := writeChunk(w, ChatCompletionChunk{
			ID:      id,
			Object:  "chat.completion.chunk",
			Created: created,
			Model:   model,
			Choices: []ChatCompletionChunkChoice{{
				Index: 0,
				Delta: ChatCompletionDelta{Content: safe},
			}},
		}); err != nil {
			return err
		}
		flusher.Flush()
	}

	stop := "stop"
	if err := writeChunk(w, ChatCompletionChunk{
		ID:      id,
		Object:  "chat.completion.chunk",
		Created: created,
		Model:   model,
		Choices: []ChatCompletionChunkChoice{{
			Index:        0,
			Delta:        ChatCompletionDelta{},
			FinishReason: &stop,
		}},
	}); err != nil {
		return err
	}
	flusher.Flush()

	if _, err := fmt.Fprint(w, "data: [DONE]\n\n"); err != nil {
		return err
	}
	flusher.Flush()
	return nil
}

func writeChunk(w http.ResponseWriter, c ChatCompletionChunk) error {
	b, err := json.Marshal(c)
	if err != nil {
		return err
	}
	_, err = fmt.Fprintf(w, "data: %s\n\n", b)
	return err
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
