package tui

import (
	"database/sql"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// historyMessage is one persisted row in the local conversation log.
type historyMessage struct {
	ID        int64
	ConvID    int64
	Role      string // "user" | "assistant"
	Content   string
	CreatedAt time.Time
}

// historyConv is one row in the conversations table — what the chat-picker
// shows.
type historyConv struct {
	ID        int64
	Title     string
	UpdatedAt time.Time
}

// historyStore is the TUI's local SQLite, kept independent from the daemon's
// torii.db so we never compete for the writer lock and the user can wipe
// their TUI history without touching the bot's state.
type historyStore struct {
	db *sql.DB
}

func defaultHistoryPath() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(home, ".local", "share", "torii", "tui-history.db"), nil
}

func newHistoryStore(path string) (*historyStore, error) {
	if err := os.MkdirAll(filepath.Dir(path), 0o700); err != nil {
		return nil, err
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, err
	}
	if _, err := db.Exec(`
		PRAGMA journal_mode=WAL;
		CREATE TABLE IF NOT EXISTS conversations (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			title TEXT NOT NULL DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			conv_id INTEGER NOT NULL,
			role TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			FOREIGN KEY (conv_id) REFERENCES conversations(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_messages_conv ON messages(conv_id);
	`); err != nil {
		_ = db.Close()
		return nil, err
	}
	return &historyStore{db: db}, nil
}

func (h *historyStore) Close() error { return h.db.Close() }

// newConversation inserts an empty conversation and returns its id. The
// title is set later from the first user message (see deriveTitle).
func (h *historyStore) newConversation() (int64, error) {
	res, err := h.db.Exec(`INSERT INTO conversations (title) VALUES ('')`)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (h *historyStore) appendMessage(convID int64, role, content string) error {
	if _, err := h.db.Exec(
		`INSERT INTO messages (conv_id, role, content) VALUES (?, ?, ?)`,
		convID, role, content,
	); err != nil {
		return err
	}
	_, err := h.db.Exec(
		`UPDATE conversations SET updated_at = CURRENT_TIMESTAMP WHERE id = ?`,
		convID,
	)
	return err
}

// setTitle replaces the conversation title. Called after the first user
// message so the chat picker has something readable to show.
func (h *historyStore) setTitle(convID int64, title string) error {
	_, err := h.db.Exec(
		`UPDATE conversations SET title = ? WHERE id = ?`,
		title, convID,
	)
	return err
}

func (h *historyStore) listMessages(convID int64) ([]historyMessage, error) {
	rows, err := h.db.Query(
		`SELECT id, conv_id, role, content, created_at FROM messages WHERE conv_id = ? ORDER BY id`,
		convID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []historyMessage
	for rows.Next() {
		var m historyMessage
		var created string
		if err := rows.Scan(&m.ID, &m.ConvID, &m.Role, &m.Content, &created); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", created)
		out = append(out, m)
	}
	return out, rows.Err()
}

func (h *historyStore) listConversations() ([]historyConv, error) {
	rows, err := h.db.Query(
		`SELECT id, title, updated_at FROM conversations ORDER BY updated_at DESC`,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []historyConv
	for rows.Next() {
		var c historyConv
		var updated string
		if err := rows.Scan(&c.ID, &c.Title, &updated); err != nil {
			return nil, err
		}
		c.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		out = append(out, c)
	}
	return out, rows.Err()
}

// deriveTitle turns the first user message into a short conversation label.
// Single line, capped to 60 runes — the chat picker truncates further.
func deriveTitle(firstUserMessage string) string {
	t := strings.TrimSpace(firstUserMessage)
	if i := strings.IndexAny(t, "\r\n"); i >= 0 {
		t = t[:i]
	}
	if len([]rune(t)) > 60 {
		r := []rune(t)
		t = string(r[:60]) + "…"
	}
	if t == "" {
		return fmt.Sprintf("untitled-%d", time.Now().Unix())
	}
	return t
}
