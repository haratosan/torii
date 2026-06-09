package tui

import (
	"fmt"
	"io"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	tea "github.com/charmbracelet/bubbletea"
	"github.com/charmbracelet/lipgloss"
)

// Style palette — picked to read on dark terminals, which is the
// overwhelming default for a CLI tool. Tuned to feel familiar to opencode
// users (cyan accents, dim borders).
var (
	headerStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9CDCFE")).
			Bold(true)

	userLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#7AA2F7")).
			Bold(true)

	assistantLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#9ECE6A")).
			Bold(true)

	toolLabel = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#E0AF68"))

	toolOk = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#9ECE6A")).
		Bold(true)

	toolFail = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F7768E")).
			Bold(true)

	statusLineStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#808080"))

	errorStyle = lipgloss.NewStyle().
			Foreground(lipgloss.Color("#F7768E"))

	inputBorder = lipgloss.NewStyle().
			BorderStyle(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("#3B3B3B")).
			Padding(0, 1)

	divider = lipgloss.NewStyle().
		Foreground(lipgloss.Color("#3B3B3B"))
)

const (
	headerRows = 1
	dividerRows = 1
	inputRows   = 3 // input box with border
	statusRows  = 1
)

// relayout fits the textinput + viewport to the current terminal size.
// Called on every WindowSizeMsg.
func (m *model) relayout() {
	if m.width <= 0 || m.height <= 0 {
		return
	}
	viewportH := m.height - headerRows - dividerRows - inputRows - statusRows
	if viewportH < 3 {
		viewportH = 3
	}
	m.view.Width = m.width
	m.view.Height = viewportH

	m.input.Width = m.width - 6 // leave room for prompt + borders

	// list takes the message area when in picker mode
	m.picker.SetSize(m.width-4, viewportH-2)

	m.rebuildView()
	m.view.GotoBottom()
}

// rebuildView renders the entire message list into the viewport. We rebuild
// from scratch on every token so tool-status transitions ("running" → "✓")
// repaint correctly. The volumes involved (tens of messages) make this a
// trivially cheap operation.
func (m *model) rebuildView() {
	if m.view.Width <= 0 {
		return
	}
	var b strings.Builder
	wrapWidth := m.view.Width

	for i, dm := range m.msgs {
		if i > 0 {
			b.WriteString("\n")
		}
		switch dm.role {
		case roleUser:
			b.WriteString(userLabel.Render("you"))
			b.WriteString("\n")
			b.WriteString(wrapPlain(dm.content, wrapWidth))
		case roleAssistant:
			b.WriteString(assistantLabel.Render("torii"))
			b.WriteString("\n")
			b.WriteString(renderMarkdown(dm.content, wrapWidth))
		case roleStatus:
			b.WriteString(errorStyle.Render(dm.content))
		}
		b.WriteString("\n")
	}

	// In-flight turn: tool calls followed by streaming text.
	if m.busy || m.streamingAsst.Len() > 0 || len(m.tools) > 0 {
		if len(m.msgs) > 0 {
			b.WriteString("\n")
		}
		b.WriteString(assistantLabel.Render("torii"))
		b.WriteString("\n")
		for _, t := range m.tools {
			b.WriteString(renderToolLine(t, wrapWidth))
			b.WriteString("\n")
		}
		if streaming := m.streamingAsst.String(); streaming != "" {
			b.WriteString(renderMarkdown(streaming, wrapWidth))
		}
	}

	m.view.SetContent(b.String())
}

// renderToolLine produces one "🔧 toolname(args)  ✓" line. Long arg blobs
// (e.g. base64) are truncated so layout doesn't blow up.
func renderToolLine(t pendingTool, width int) string {
	args := singleLine(t.args)
	maxArgs := width / 2
	if maxArgs < 20 {
		maxArgs = 20
	}
	if len([]rune(args)) > maxArgs {
		args = string([]rune(args)[:maxArgs]) + "…"
	}
	header := fmt.Sprintf("🔧 %s(%s)", t.name, args)
	switch {
	case !t.done:
		return toolLabel.Render(header) + statusLineStyle.Render("  …")
	case t.ok:
		return toolLabel.Render(header) + "  " + toolOk.Render("✓")
	default:
		errMsg := t.errStr
		if errMsg == "" {
			errMsg = "failed"
		}
		errMsg = singleLine(errMsg)
		if len([]rune(errMsg)) > 60 {
			errMsg = string([]rune(errMsg)[:60]) + "…"
		}
		return toolLabel.Render(header) + "  " + toolFail.Render("✗ "+errMsg)
	}
}

// singleLine collapses newlines so a multi-line arg blob doesn't break the
// tool-line render.
func singleLine(s string) string {
	s = strings.ReplaceAll(s, "\r", "")
	s = strings.ReplaceAll(s, "\n", " ")
	return strings.TrimSpace(s)
}

// wrapPlain hard-wraps user-input lines to the given width without
// glamour. User messages aren't markdown — we want them shown verbatim.
func wrapPlain(s string, width int) string {
	if width <= 0 {
		return s
	}
	var out strings.Builder
	for _, line := range strings.Split(s, "\n") {
		if len([]rune(line)) <= width {
			out.WriteString(line)
			out.WriteString("\n")
			continue
		}
		runes := []rune(line)
		for i := 0; i < len(runes); i += width {
			end := i + width
			if end > len(runes) {
				end = len(runes)
			}
			out.WriteString(string(runes[i:end]))
			out.WriteString("\n")
		}
	}
	return strings.TrimRight(out.String(), "\n")
}

func (m model) View() string {
	if m.width == 0 || m.height == 0 {
		return "starting torii tui…"
	}

	header := m.renderHeader()
	div := divider.Render(strings.Repeat("─", m.width))

	var middle string
	if m.mode == modePicker {
		middle = lipgloss.NewStyle().
			Width(m.width).
			Height(m.view.Height).
			Render(m.picker.View())
	} else {
		middle = m.view.View()
	}

	input := inputBorder.Width(m.width - 2).Render(m.input.View())
	status := m.renderStatusLine()

	return lipgloss.JoinVertical(lipgloss.Left,
		header,
		div,
		middle,
		input,
		status,
	)
}

func (m model) renderHeader() string {
	label := fmt.Sprintf("torii · %s", m.client.Model)
	if m.mode == modePicker {
		label += " · picker"
	}
	return headerStyle.Render(label)
}

func (m model) renderStatusLine() string {
	left := m.scanner.render()
	if !m.busy {
		left = scannerOff.Render(strings.Repeat("·", scannerWidth))
	}
	hints := []string{"⏎ senden", "^c " + ifElse(m.busy, "abbrechen", "quit"), "^n neuer chat", "^l chats"}
	right := statusLineStyle.Render(strings.Join(hints, " · "))

	gap := m.width - lipgloss.Width(left) - lipgloss.Width(right) - 2
	if gap < 1 {
		gap = 1
	}
	return "  " + left + strings.Repeat(" ", gap) + right
}

func ifElse(cond bool, a, b string) string {
	if cond {
		return a
	}
	return b
}

// conversationDelegate renders one row in the picker list: title + last-used
// timestamp. Bubbles ships a default but it's vertically chatty — this is a
// terse single-line variant.
type conversationDelegate struct{}

func (conversationDelegate) Height() int                                 { return 1 }
func (conversationDelegate) Spacing() int                                { return 0 }
func (conversationDelegate) Update(_ tea.Msg, _ *list.Model) tea.Cmd     { return nil }
func (d conversationDelegate) Render(w io.Writer, m list.Model, index int, item list.Item) {
	c, ok := item.(convItem)
	if !ok {
		return
	}
	prefix := "  "
	if index == m.Index() {
		prefix = "▶ "
	}
	when := c.c.UpdatedAt.Format("2006-01-02 15:04")
	title := c.c.Title
	if title == "" {
		title = "(unbenannt)"
	}
	line := fmt.Sprintf("%s%s  %s", prefix, when, title)
	if index == m.Index() {
		line = headerStyle.Render(line)
	}
	fmt.Fprint(w, line)
}
