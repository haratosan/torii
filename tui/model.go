package tui

import (
	"context"
	"strings"

	"github.com/charmbracelet/bubbles/list"
	"github.com/charmbracelet/bubbles/textinput"
	"github.com/charmbracelet/bubbles/viewport"
	tea "github.com/charmbracelet/bubbletea"
)

// mode controls what the model is currently showing: the chat itself or
// the conversation-picker overlay.
type mode int

const (
	modeChat mode = iota
	modePicker
)

// displayRole separates the renderer's logic from the wire format so we can
// add e.g. "system" later without touching the API mapping.
type displayRole string

const (
	roleUser      displayRole = "user"
	roleAssistant displayRole = "assistant"
	roleStatus    displayRole = "status" // tool calls, errors
)

type displayMessage struct {
	role    displayRole
	content string
}

// pendingTool holds the in-flight state for a single tool call so the view
// can show "running…" first and then "✓" or "✗" once the result arrives.
type pendingTool struct {
	name   string
	args   string
	done   bool
	ok     bool
	output string
	errStr string
}

type model struct {
	client  *Client
	history *historyStore

	// session state
	convID        int64
	titleSet      bool
	msgs          []displayMessage
	tools         []pendingTool
	streamingAsst strings.Builder

	// ui state
	width   int
	height  int
	mode    mode
	input   textinput.Model
	view    viewport.Model
	picker  list.Model
	busy    bool
	scanner scanner
	status  string

	// stream state. activeCh and streamCancel are paired — both nil when
	// no request is in flight.
	activeCh     <-chan tea.Msg
	streamCancel context.CancelFunc
}

func newModel(client *Client, hist *historyStore, initialConvID int64) model {
	ti := textinput.New()
	ti.Placeholder = "Frag torii etwas…"
	ti.Focus()
	ti.Prompt = "❯ "
	ti.CharLimit = 8000

	vp := viewport.New(80, 20)

	li := list.New(nil, conversationDelegate{}, 60, 14)
	li.Title = "Konversationen"
	li.SetShowStatusBar(false)
	li.SetFilteringEnabled(false)
	li.SetShowHelp(false)

	return model{
		client:  client,
		history: hist,
		convID:  initialConvID,
		mode:    modeChat,
		input:   ti,
		view:    vp,
		picker:  li,
		scanner: newScanner(),
	}
}

func (m model) Init() tea.Cmd { return textinput.Blink }

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = msg.Width
		m.height = msg.Height
		m.relayout()
		return m, nil

	case tea.KeyMsg:
		return m.handleKey(msg)

	case scannerTickMsg:
		if !m.busy {
			return m, nil
		}
		m.scanner.step()
		return m, scannerTickCmd()

	case StreamTokenMsg:
		m.streamingAsst.WriteString(msg.Text)
		m.rebuildView()
		m.view.GotoBottom()
		return m, m.armNextStreamCmd()

	case StreamToolCallMsg:
		m.tools = append(m.tools, pendingTool{name: msg.Name, args: msg.Args})
		m.rebuildView()
		m.view.GotoBottom()
		return m, m.armNextStreamCmd()

	case StreamToolResultMsg:
		// Match to the most recent unfinished call with the same name. If
		// none, append a synthetic entry — the daemon should never let this
		// happen but we guard against drift.
		matched := false
		for i := len(m.tools) - 1; i >= 0; i-- {
			if m.tools[i].name == msg.Name && !m.tools[i].done {
				m.tools[i].done = true
				m.tools[i].ok = msg.Err == ""
				m.tools[i].output = msg.Output
				m.tools[i].errStr = msg.Err
				matched = true
				break
			}
		}
		if !matched {
			m.tools = append(m.tools, pendingTool{
				name: msg.Name, done: true, ok: msg.Err == "",
				output: msg.Output, errStr: msg.Err,
			})
		}
		m.rebuildView()
		m.view.GotoBottom()
		return m, m.armNextStreamCmd()

	case StreamErrorMsg:
		m.busy = false
		m.streamCancel = nil
		m.activeCh = nil
		m.streamingAsst.Reset()
		m.msgs = append(m.msgs, displayMessage{
			role:    roleStatus,
			content: "Fehler: " + msg.Err.Error(),
		})
		m.rebuildView()
		return m, nil

	case StreamDoneMsg:
		// Persist the assistant's final answer and lock in the turn.
		text := strings.TrimSpace(m.streamingAsst.String())
		if text != "" {
			m.msgs = append(m.msgs, displayMessage{role: roleAssistant, content: text})
			if m.history != nil {
				_ = m.history.appendMessage(m.convID, "assistant", text)
			}
		}
		m.busy = false
		m.streamCancel = nil
		m.activeCh = nil
		m.streamingAsst.Reset()
		m.tools = nil
		m.rebuildView()
		return m, nil

	case conversationsLoadedMsg:
		items := make([]list.Item, 0, len(msg.convs))
		for _, c := range msg.convs {
			items = append(items, convItem{c: c})
		}
		m.picker.SetItems(items)
		return m, nil
	}

	// Forward everything else to the active subcomponent so cursor blinks,
	// list scrolling etc. keep working.
	var cmd tea.Cmd
	if m.mode == modePicker {
		m.picker, cmd = m.picker.Update(msg)
	} else {
		m.input, cmd = m.input.Update(msg)
	}
	return m, cmd
}

func (m model) handleKey(msg tea.KeyMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		if m.busy && m.streamCancel != nil {
			// First Ctrl+C interrupts the in-flight stream rather than quitting
			// the TUI; second one (when not busy) quits.
			m.streamCancel()
			return m, nil
		}
		return m, tea.Quit
	case "ctrl+n":
		return m.startNewConversation()
	case "ctrl+l":
		return m.openPicker()
	}

	if m.mode == modePicker {
		switch msg.String() {
		case "esc":
			m.mode = modeChat
			return m, nil
		case "enter":
			return m.selectPickedConversation()
		}
		var cmd tea.Cmd
		m.picker, cmd = m.picker.Update(msg)
		return m, cmd
	}

	if msg.String() == "enter" {
		return m.submit()
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// armNextStreamCmd re-queues a wait on the active channel for the next
// event. Returns nil when no stream is in flight (defensive — Done/Error
// already cleared activeCh).
func (m model) armNextStreamCmd() tea.Cmd {
	if m.activeCh == nil {
		return nil
	}
	return waitForStreamMsg(m.activeCh)
}

// submit fires the current input as a new user turn. No-op while busy.
func (m model) submit() (tea.Model, tea.Cmd) {
	if m.busy {
		return m, nil
	}
	text := strings.TrimSpace(m.input.Value())
	if text == "" {
		return m, nil
	}
	m.input.SetValue("")

	m.msgs = append(m.msgs, displayMessage{role: roleUser, content: text})
	m.streamingAsst.Reset()
	m.tools = nil

	if m.history != nil {
		_ = m.history.appendMessage(m.convID, "user", text)
		if !m.titleSet {
			_ = m.history.setTitle(m.convID, deriveTitle(text))
			m.titleSet = true
		}
	}

	// Build the full history payload the stateless API expects. Skip
	// status-role entries (errors, banners) — they're TUI-local artifacts.
	wire := make([]chatMessage, 0, len(m.msgs))
	for _, dm := range m.msgs {
		if dm.role == roleStatus {
			continue
		}
		wire = append(wire, chatMessage{Role: string(dm.role), Content: dm.content})
	}

	ctx, cancel := context.WithCancel(context.Background())
	m.streamCancel = cancel
	m.activeCh = m.client.Stream(ctx, wire)

	m.busy = true
	m.scanner = newScanner()
	m.rebuildView()
	m.view.GotoBottom()
	return m, tea.Batch(
		waitForStreamMsg(m.activeCh),
		scannerTickCmd(),
	)
}

// startNewConversation clears the on-screen state and opens a fresh row in
// the history DB. Old conversations remain accessible via the picker.
func (m model) startNewConversation() (tea.Model, tea.Cmd) {
	if m.busy && m.streamCancel != nil {
		m.streamCancel()
		m.busy = false
		m.activeCh = nil
	}
	if m.history != nil {
		if id, err := m.history.newConversation(); err == nil {
			m.convID = id
		}
	}
	m.msgs = nil
	m.tools = nil
	m.streamingAsst.Reset()
	m.titleSet = false
	m.rebuildView()
	return m, nil
}

// openPicker switches into the conversation-list view and loads the rows
// from history. The list paints empty until conversationsLoadedMsg fires.
func (m model) openPicker() (tea.Model, tea.Cmd) {
	if m.history == nil {
		return m, nil
	}
	m.mode = modePicker
	return m, loadConversationsCmd(m.history)
}

func (m model) selectPickedConversation() (tea.Model, tea.Cmd) {
	item, ok := m.picker.SelectedItem().(convItem)
	if !ok {
		return m, nil
	}
	if m.busy && m.streamCancel != nil {
		m.streamCancel()
	}
	msgs, err := m.history.listMessages(item.c.ID)
	if err != nil {
		m.msgs = append(m.msgs, displayMessage{
			role:    roleStatus,
			content: "Konversation konnte nicht geladen werden: " + err.Error(),
		})
	} else {
		m.msgs = nil
		for _, hm := range msgs {
			m.msgs = append(m.msgs, displayMessage{
				role:    displayRole(hm.Role),
				content: hm.Content,
			})
		}
	}
	m.convID = item.c.ID
	m.titleSet = item.c.Title != ""
	m.tools = nil
	m.streamingAsst.Reset()
	m.mode = modeChat
	m.busy = false
	m.activeCh = nil
	m.streamCancel = nil
	m.rebuildView()
	m.view.GotoBottom()
	return m, nil
}

// conversation-list plumbing — kept here so model.go owns all msg types.

type conversationsLoadedMsg struct{ convs []historyConv }

func loadConversationsCmd(h *historyStore) tea.Cmd {
	return func() tea.Msg {
		convs, err := h.listConversations()
		if err != nil {
			return StreamErrorMsg{Err: err}
		}
		return conversationsLoadedMsg{convs: convs}
	}
}

type convItem struct{ c historyConv }

func (c convItem) FilterValue() string { return c.c.Title }
