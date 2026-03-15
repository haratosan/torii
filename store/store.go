package store

import (
	"database/sql"
	"time"

	_ "modernc.org/sqlite"
)

type Task struct {
	ID          int64
	Type        string
	ChatID      string
	UserID      string
	Description string
	Schedule    string
	NextRun     time.Time
	OneShot     bool
	CreatedAt   time.Time
}

type Store struct {
	db *sql.DB
}

func New(dbPath string) (*Store, error) {
	db, err := sql.Open("sqlite", dbPath)
	if err != nil {
		return nil, err
	}

	if _, err := db.Exec("PRAGMA journal_mode=WAL"); err != nil {
		db.Close()
		return nil, err
	}

	s := &Store{db: db}
	if err := s.migrate(); err != nil {
		db.Close()
		return nil, err
	}
	return s, nil
}

func (s *Store) Close() error {
	return s.db.Close()
}

func (s *Store) migrate() error {
	_, err := s.db.Exec(`
		CREATE TABLE IF NOT EXISTS tasks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			type TEXT NOT NULL,
			chat_id TEXT NOT NULL,
			user_id TEXT NOT NULL,
			description TEXT NOT NULL,
			schedule TEXT DEFAULT '',
			next_run DATETIME NOT NULL,
			one_shot INTEGER DEFAULT 0,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS user_memory (
			user_id TEXT PRIMARY KEY,
			notes TEXT DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS bot_profile (
			key TEXT PRIMARY KEY,
			value TEXT NOT NULL,
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE TABLE IF NOT EXISTS session_messages (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id TEXT NOT NULL,
			role TEXT NOT NULL,
			content TEXT DEFAULT '',
			tool_calls TEXT DEFAULT '',
			tool_call_id TEXT DEFAULT '',
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_session_messages_chat_id ON session_messages(chat_id);

		CREATE TABLE IF NOT EXISTS kb_documents (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			chat_id TEXT NOT NULL,
			title TEXT NOT NULL,
			content TEXT NOT NULL,
			created_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_kb_documents_chat_id ON kb_documents(chat_id);

		CREATE TABLE IF NOT EXISTS kb_chunks (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			document_id INTEGER NOT NULL,
			chat_id TEXT NOT NULL,
			content TEXT NOT NULL,
			embedding BLOB,
			FOREIGN KEY (document_id) REFERENCES kb_documents(id) ON DELETE CASCADE
		);
		CREATE INDEX IF NOT EXISTS idx_kb_chunks_chat_id ON kb_chunks(chat_id);
	`)
	return err
}

// --- Tasks ---

func (s *Store) CreateTask(t *Task) error {
	res, err := s.db.Exec(
		`INSERT INTO tasks (type, chat_id, user_id, description, schedule, next_run, one_shot) VALUES (?, ?, ?, ?, ?, ?, ?)`,
		t.Type, t.ChatID, t.UserID, t.Description, t.Schedule, t.NextRun.UTC().Format(time.RFC3339), boolToInt(t.OneShot),
	)
	if err != nil {
		return err
	}
	t.ID, _ = res.LastInsertId()
	return nil
}

func (s *Store) DeleteTask(id int64) error {
	_, err := s.db.Exec(`DELETE FROM tasks WHERE id = ?`, id)
	return err
}

func (s *Store) DeleteTaskByChat(id int64, chatID string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM tasks WHERE id = ? AND chat_id = ?`, id, chatID)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) ListTasksByChat(chatID string) ([]*Task, error) {
	rows, err := s.db.Query(
		`SELECT id, type, chat_id, user_id, description, schedule, next_run, one_shot, created_at FROM tasks WHERE chat_id = ? ORDER BY id`,
		chatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *Store) DueTasks(now time.Time) ([]*Task, error) {
	rows, err := s.db.Query(
		`SELECT id, type, chat_id, user_id, description, schedule, next_run, one_shot, created_at FROM tasks WHERE next_run <= ?`,
		now.UTC().Format(time.RFC3339),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
}

func (s *Store) UpdateNextRun(id int64, nextRun time.Time) error {
	_, err := s.db.Exec(`UPDATE tasks SET next_run = ? WHERE id = ?`, nextRun.UTC().Format(time.RFC3339), id)
	return err
}

func scanTasks(rows *sql.Rows) ([]*Task, error) {
	var tasks []*Task
	for rows.Next() {
		t := &Task{}
		var oneShot int
		var nextRun, createdAt string
		if err := rows.Scan(&t.ID, &t.Type, &t.ChatID, &t.UserID, &t.Description, &t.Schedule, &nextRun, &oneShot, &createdAt); err != nil {
			return nil, err
		}
		t.OneShot = oneShot == 1
		t.NextRun, _ = time.Parse(time.RFC3339, nextRun)
		t.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		tasks = append(tasks, t)
	}
	return tasks, rows.Err()
}

// --- User Memory ---

func (s *Store) GetMemory(userID string) (string, error) {
	var notes string
	err := s.db.QueryRow(`SELECT notes FROM user_memory WHERE user_id = ?`, userID).Scan(&notes)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return notes, err
}

func (s *Store) SetMemory(userID string, notes string) error {
	_, err := s.db.Exec(
		`INSERT INTO user_memory (user_id, notes, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(user_id) DO UPDATE SET notes = excluded.notes, updated_at = excluded.updated_at`,
		userID, notes,
	)
	return err
}

func (s *Store) DeleteMemory(userID string) error {
	_, err := s.db.Exec(`DELETE FROM user_memory WHERE user_id = ?`, userID)
	return err
}

func (s *Store) HasMemory(userID string) (bool, error) {
	var count int
	err := s.db.QueryRow(`SELECT COUNT(*) FROM user_memory WHERE user_id = ?`, userID).Scan(&count)
	return count > 0, err
}

// --- Bot Profile ---

func (s *Store) GetBotProfile(key string) (string, error) {
	var value string
	err := s.db.QueryRow(`SELECT value FROM bot_profile WHERE key = ?`, key).Scan(&value)
	if err == sql.ErrNoRows {
		return "", nil
	}
	return value, err
}

func (s *Store) SetBotProfile(key string, value string) error {
	_, err := s.db.Exec(
		`INSERT INTO bot_profile (key, value, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(key) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at`,
		key, value,
	)
	return err
}

func (s *Store) GetAllBotProfile() (map[string]string, error) {
	rows, err := s.db.Query(`SELECT key, value FROM bot_profile`)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	result := make(map[string]string)
	for rows.Next() {
		var k, v string
		if err := rows.Scan(&k, &v); err != nil {
			return nil, err
		}
		result[k] = v
	}
	return result, rows.Err()
}

// --- Session Messages ---

type SessionMessage struct {
	ID         int64
	ChatID     string
	Role       string
	Content    string
	ToolCalls  string
	ToolCallID string
}

func (s *Store) SaveMessage(chatID string, role string, content string, toolCalls string, toolCallID string) error {
	_, err := s.db.Exec(
		`INSERT INTO session_messages (chat_id, role, content, tool_calls, tool_call_id) VALUES (?, ?, ?, ?, ?)`,
		chatID, role, content, toolCalls, toolCallID,
	)
	return err
}

func (s *Store) LoadMessages(chatID string, limit int) ([]SessionMessage, error) {
	rows, err := s.db.Query(
		`SELECT id, chat_id, role, content, tool_calls, tool_call_id FROM session_messages WHERE chat_id = ? ORDER BY id DESC LIMIT ?`,
		chatID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []SessionMessage
	for rows.Next() {
		var m SessionMessage
		if err := rows.Scan(&m.ID, &m.ChatID, &m.Role, &m.Content, &m.ToolCalls, &m.ToolCallID); err != nil {
			return nil, err
		}
		msgs = append(msgs, m)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reverse to chronological order
	for i, j := 0, len(msgs)-1; i < j; i, j = i+1, j-1 {
		msgs[i], msgs[j] = msgs[j], msgs[i]
	}
	return msgs, nil
}

func (s *Store) DeleteMessages(chatID string) error {
	_, err := s.db.Exec(`DELETE FROM session_messages WHERE chat_id = ?`, chatID)
	return err
}

func (s *Store) TrimMessages(chatID string, keep int) error {
	_, err := s.db.Exec(
		`DELETE FROM session_messages WHERE chat_id = ? AND id NOT IN (SELECT id FROM session_messages WHERE chat_id = ? ORDER BY id DESC LIMIT ?)`,
		chatID, chatID, keep,
	)
	return err
}

// --- Knowledge Base ---

type KBDocument struct {
	ID        int64
	ChatID    string
	Title     string
	Content   string
	CreatedAt string
}

type KBChunk struct {
	ID         int64
	DocumentID int64
	ChatID     string
	Content    string
	Embedding  []byte
}

func (s *Store) CreateKBDocument(chatID string, title string, content string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO kb_documents (chat_id, title, content) VALUES (?, ?, ?)`,
		chatID, title, content,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

func (s *Store) ListKBDocuments(chatID string) ([]KBDocument, error) {
	rows, err := s.db.Query(
		`SELECT id, chat_id, title, content, created_at FROM kb_documents WHERE chat_id = ? ORDER BY id`,
		chatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var docs []KBDocument
	for rows.Next() {
		var d KBDocument
		if err := rows.Scan(&d.ID, &d.ChatID, &d.Title, &d.Content, &d.CreatedAt); err != nil {
			return nil, err
		}
		docs = append(docs, d)
	}
	return docs, rows.Err()
}

func (s *Store) GetKBDocumentTitle(docID int64) (string, error) {
	var title string
	err := s.db.QueryRow(`SELECT title FROM kb_documents WHERE id = ?`, docID).Scan(&title)
	if err != nil {
		return "", err
	}
	return title, nil
}

func (s *Store) DeleteKBDocument(chatID string, docID int64) error {
	// Delete chunks first
	if _, err := s.db.Exec(`DELETE FROM kb_chunks WHERE document_id = ? AND chat_id = ?`, docID, chatID); err != nil {
		return err
	}
	_, err := s.db.Exec(`DELETE FROM kb_documents WHERE id = ? AND chat_id = ?`, docID, chatID)
	return err
}

func (s *Store) CreateKBChunk(docID int64, chatID string, content string, embedding []byte) error {
	_, err := s.db.Exec(
		`INSERT INTO kb_chunks (document_id, chat_id, content, embedding) VALUES (?, ?, ?, ?)`,
		docID, chatID, content, embedding,
	)
	return err
}

func (s *Store) ListKBChunks(chatID string) ([]KBChunk, error) {
	rows, err := s.db.Query(
		`SELECT id, document_id, chat_id, content, embedding FROM kb_chunks WHERE chat_id = ?`,
		chatID,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var chunks []KBChunk
	for rows.Next() {
		var c KBChunk
		if err := rows.Scan(&c.ID, &c.DocumentID, &c.ChatID, &c.Content, &c.Embedding); err != nil {
			return nil, err
		}
		chunks = append(chunks, c)
	}
	return chunks, rows.Err()
}

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
