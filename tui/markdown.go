package tui

import (
	"strings"
	"sync"

	"github.com/charmbracelet/glamour"
)

// Glamour's renderer is expensive to build AND, with auto-style, probes the
// terminal for background color + cursor position via DSR escapes. Inside a
// Bubbletea program the reply comes back through the same TTY that
// Bubbletea's input reader owns, so the terminal's "ESC[51;1R" answer ends
// up routed into the focused textinput as keystrokes. Two consequences:
// build the renderer once per width, and stick to the dark style so no
// probing happens at all.

var (
	mdMu       sync.Mutex
	mdRenderer *glamour.TermRenderer
	mdWidth    int
)

func markdownRenderer(width int) *glamour.TermRenderer {
	mdMu.Lock()
	defer mdMu.Unlock()
	if mdRenderer != nil && mdWidth == width {
		return mdRenderer
	}
	r, err := glamour.NewTermRenderer(
		glamour.WithStandardStyle("dark"),
		glamour.WithWordWrap(width),
		glamour.WithEmoji(),
	)
	if err != nil {
		return nil
	}
	mdRenderer = r
	mdWidth = width
	return r
}

// renderMarkdown turns the assistant's text into terminal-rendered output.
// On any glamour error (rare — invalid style, no TTY) we fall back to the
// raw text so the user never sees a blank pane.
func renderMarkdown(text string, width int) string {
	if width <= 0 {
		width = 80
	}
	r := markdownRenderer(width)
	if r == nil {
		return text
	}
	out, err := r.Render(text)
	if err != nil {
		return text
	}
	// glamour adds a trailing blank line; trim to keep message density up.
	return strings.TrimRight(out, "\n")
}
