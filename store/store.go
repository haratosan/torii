package store

import (
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

// ErrMemoryFull is returned by AppendMemoryLine when the new line would push
// the total notes blob past the configured maxChars budget.
var ErrMemoryFull = errors.New("memory full")

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
		CREATE INDEX IF NOT EXISTS idx_tasks_next_run ON tasks(next_run);
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

		CREATE TABLE IF NOT EXISTS agent_skills (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			scope TEXT NOT NULL,
			title TEXT NOT NULL,
			body TEXT NOT NULL DEFAULT '',
			updated_at DATETIME DEFAULT CURRENT_TIMESTAMP
		);
		CREATE INDEX IF NOT EXISTS idx_agent_skills_scope ON agent_skills(scope);
		CREATE UNIQUE INDEX IF NOT EXISTS idx_agent_skills_scope_title ON agent_skills(scope, title);

		CREATE TABLE IF NOT EXISTS evolution_runs (
			id INTEGER PRIMARY KEY AUTOINCREMENT,
			user_id TEXT NOT NULL,
			started_at DATETIME DEFAULT CURRENT_TIMESTAMP,
			finished_at DATETIME,
			summary TEXT DEFAULT '',
			status TEXT DEFAULT 'running'
		);
		CREATE INDEX IF NOT EXISTS idx_evo_user_started ON evolution_runs(user_id, started_at DESC);
	`)
	if err != nil {
		return err
	}

	// Idempotent column-add for trace data: session_messages got a user_id
	// after the initial release, so we widen via ALTER TABLE on existing
	// installs and skip the no-op on fresh ones.
	hasUserID, err := s.columnExists("session_messages", "user_id")
	if err != nil {
		return err
	}
	if !hasUserID {
		if _, err := s.db.Exec(`ALTER TABLE session_messages ADD COLUMN user_id TEXT DEFAULT ''`); err != nil {
			return err
		}
	}
	return nil
}

// columnExists checks PRAGMA table_info to decide whether an ALTER is needed.
func (s *Store) columnExists(table, col string) (bool, error) {
	rows, err := s.db.Query(fmt.Sprintf(`PRAGMA table_info(%s)`, table))
	if err != nil {
		return false, err
	}
	defer rows.Close()
	for rows.Next() {
		var (
			cid     int
			name    string
			ctype   string
			notnull int
			dflt    sql.NullString
			pk      int
		)
		if err := rows.Scan(&cid, &name, &ctype, &notnull, &dflt, &pk); err != nil {
			return false, err
		}
		if name == col {
			return true, nil
		}
	}
	return false, rows.Err()
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

// splitMemoryLines breaks the stored notes blob into trimmed, non-empty lines.
// Forward-compatible with legacy single-blob rows: a blob without newlines
// just becomes a single line.
func splitMemoryLines(blob string) []string {
	if blob == "" {
		return nil
	}
	raw := strings.Split(blob, "\n")
	out := make([]string, 0, len(raw))
	for _, l := range raw {
		l = strings.TrimSpace(l)
		if l != "" {
			out = append(out, l)
		}
	}
	return out
}

func joinMemoryLines(lines []string) string {
	return strings.Join(lines, "\n")
}

// GetMemoryLines returns the user's notes split into one trimmed, non-empty
// line per fact.
func (s *Store) GetMemoryLines(userID string) ([]string, error) {
	blob, err := s.GetMemory(userID)
	if err != nil {
		return nil, err
	}
	return splitMemoryLines(blob), nil
}

// AppendMemoryLine appends a single fact as a new line. Internal newlines are
// collapsed to spaces so each fact stays on one line. If the resulting blob
// would exceed maxChars, no write happens and ErrMemoryFull is returned. The
// returned int is the new total char count (or the would-be total when
// rejected).
func (s *Store) AppendMemoryLine(userID, line string, maxChars int) (int, error) {
	line = strings.ReplaceAll(strings.TrimSpace(line), "\n", " ")
	if line == "" {
		return 0, fmt.Errorf("line is empty")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	var blob string
	err = tx.QueryRow(`SELECT notes FROM user_memory WHERE user_id = ?`, userID).Scan(&blob)
	if err != nil && err != sql.ErrNoRows {
		return 0, err
	}

	lines := splitMemoryLines(blob)
	lines = append(lines, line)
	merged := joinMemoryLines(lines)

	if maxChars > 0 && len(merged) > maxChars {
		return len(merged), ErrMemoryFull
	}

	if _, err := tx.Exec(
		`INSERT INTO user_memory (user_id, notes, updated_at) VALUES (?, ?, CURRENT_TIMESTAMP)
		 ON CONFLICT(user_id) DO UPDATE SET notes = excluded.notes, updated_at = excluded.updated_at`,
		userID, merged,
	); err != nil {
		return 0, err
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return len(merged), nil
}

// ReplaceMemoryLine replaces the FIRST line containing needle (case-insensitive
// substring) with replacement. Newlines in replacement are collapsed.
func (s *Store) ReplaceMemoryLine(userID, needle, replacement string) (matched string, ok bool, err error) {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return "", false, fmt.Errorf("needle is empty")
	}
	replacement = strings.ReplaceAll(strings.TrimSpace(replacement), "\n", " ")
	if replacement == "" {
		return "", false, fmt.Errorf("replacement is empty")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()

	var blob string
	err = tx.QueryRow(`SELECT notes FROM user_memory WHERE user_id = ?`, userID).Scan(&blob)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	lines := splitMemoryLines(blob)
	needleLower := strings.ToLower(needle)
	for i, l := range lines {
		if strings.Contains(strings.ToLower(l), needleLower) {
			matched = l
			lines[i] = replacement
			merged := joinMemoryLines(lines)
			if _, err := tx.Exec(
				`UPDATE user_memory SET notes = ?, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`,
				merged, userID,
			); err != nil {
				return "", false, err
			}
			if err := tx.Commit(); err != nil {
				return "", false, err
			}
			return matched, true, nil
		}
	}
	return "", false, nil
}

// RemoveMemoryLine removes the FIRST line containing needle (case-insensitive
// substring). Returns the removed line if any.
func (s *Store) RemoveMemoryLine(userID, needle string) (removed string, ok bool, err error) {
	needle = strings.TrimSpace(needle)
	if needle == "" {
		return "", false, fmt.Errorf("needle is empty")
	}

	tx, err := s.db.Begin()
	if err != nil {
		return "", false, err
	}
	defer tx.Rollback()

	var blob string
	err = tx.QueryRow(`SELECT notes FROM user_memory WHERE user_id = ?`, userID).Scan(&blob)
	if err == sql.ErrNoRows {
		return "", false, nil
	}
	if err != nil {
		return "", false, err
	}

	lines := splitMemoryLines(blob)
	needleLower := strings.ToLower(needle)
	for i, l := range lines {
		if strings.Contains(strings.ToLower(l), needleLower) {
			removed = l
			lines = append(lines[:i], lines[i+1:]...)
			merged := joinMemoryLines(lines)
			if _, err := tx.Exec(
				`UPDATE user_memory SET notes = ?, updated_at = CURRENT_TIMESTAMP WHERE user_id = ?`,
				merged, userID,
			); err != nil {
				return "", false, err
			}
			if err := tx.Commit(); err != nil {
				return "", false, err
			}
			return removed, true, nil
		}
	}
	return "", false, nil
}

// --- Agent Skills ---

type Skill struct {
	ID        int64
	Scope     string
	Title     string
	Body      string
	UpdatedAt time.Time
}

// AddSkill inserts a new skill. Returns an error on (scope,title) conflict so
// the caller (LLM) can fall back to UpdateSkill.
func (s *Store) AddSkill(scope, title, body string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO agent_skills (scope, title, body) VALUES (?, ?, ?)`,
		scope, title, body,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// UpdateSkill replaces a skill's body. The scope guard prevents cross-user
// edits — callers must pass the scope they own ('global' or 'user:<id>').
func (s *Store) UpdateSkill(id int64, scope, body string) (bool, error) {
	res, err := s.db.Exec(
		`UPDATE agent_skills SET body = ?, updated_at = CURRENT_TIMESTAMP WHERE id = ? AND scope = ?`,
		body, id, scope,
	)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

func (s *Store) RemoveSkill(id int64, scope string) (bool, error) {
	res, err := s.db.Exec(`DELETE FROM agent_skills WHERE id = ? AND scope = ?`, id, scope)
	if err != nil {
		return false, err
	}
	n, _ := res.RowsAffected()
	return n > 0, nil
}

// GetSkill fetches one skill if its scope is in the allowed scopes set.
func (s *Store) GetSkill(id int64, scopes []string) (*Skill, error) {
	if len(scopes) == 0 {
		return nil, sql.ErrNoRows
	}
	placeholders := strings.Repeat("?,", len(scopes))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(scopes)+1)
	args = append(args, id)
	for _, sc := range scopes {
		args = append(args, sc)
	}
	row := s.db.QueryRow(
		`SELECT id, scope, title, body, updated_at FROM agent_skills WHERE id = ? AND scope IN (`+placeholders+`)`,
		args...,
	)
	var sk Skill
	var updated string
	if err := row.Scan(&sk.ID, &sk.Scope, &sk.Title, &sk.Body, &updated); err != nil {
		return nil, err
	}
	sk.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
	return &sk, nil
}

// ListSkills returns all skills whose scope is in the given set, ordered by
// scope then id (so 'global' skills surface first when scopes = ['global','user:..']).
func (s *Store) ListSkills(scopes []string) ([]Skill, error) {
	if len(scopes) == 0 {
		return nil, nil
	}
	placeholders := strings.Repeat("?,", len(scopes))
	placeholders = placeholders[:len(placeholders)-1]
	args := make([]any, 0, len(scopes))
	for _, sc := range scopes {
		args = append(args, sc)
	}
	rows, err := s.db.Query(
		`SELECT id, scope, title, body, updated_at FROM agent_skills WHERE scope IN (`+placeholders+`) ORDER BY scope, id`,
		args...,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []Skill
	for rows.Next() {
		var sk Skill
		var updated string
		if err := rows.Scan(&sk.ID, &sk.Scope, &sk.Title, &sk.Body, &updated); err != nil {
			return nil, err
		}
		sk.UpdatedAt, _ = time.Parse("2006-01-02 15:04:05", updated)
		out = append(out, sk)
	}
	return out, rows.Err()
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
	UserID     string
	Role       string
	Content    string
	ToolCalls  string
	ToolCallID string
	CreatedAt  time.Time
}

func (s *Store) SaveMessage(chatID, userID, role, content, toolCalls, toolCallID string) error {
	_, err := s.db.Exec(
		`INSERT INTO session_messages (chat_id, user_id, role, content, tool_calls, tool_call_id) VALUES (?, ?, ?, ?, ?, ?)`,
		chatID, userID, role, content, toolCalls, toolCallID,
	)
	return err
}

func (s *Store) LoadMessages(chatID string, limit int) ([]SessionMessage, error) {
	rows, err := s.db.Query(
		`SELECT id, chat_id, COALESCE(user_id, ''), role, content, tool_calls, tool_call_id, created_at FROM session_messages WHERE chat_id = ? ORDER BY id DESC LIMIT ?`,
		chatID, limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []SessionMessage
	for rows.Next() {
		var m SessionMessage
		var createdAt string
		if err := rows.Scan(&m.ID, &m.ChatID, &m.UserID, &m.Role, &m.Content, &m.ToolCalls, &m.ToolCallID, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
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

// LoadMessagesByUserSince returns messages across all chat_ids written for a
// given user_id since `since`, in chronological order. Used by the
// self-evolution loop to build a per-user trace summary.
func (s *Store) LoadMessagesByUserSince(userID string, since time.Time, limit int) ([]SessionMessage, error) {
	if userID == "" {
		return nil, nil
	}
	rows, err := s.db.Query(
		`SELECT id, chat_id, COALESCE(user_id, ''), role, content, tool_calls, tool_call_id, created_at
		   FROM session_messages
		  WHERE user_id = ? AND created_at >= ?
		  ORDER BY id ASC
		  LIMIT ?`,
		userID, since.UTC().Format("2006-01-02 15:04:05"), limit,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()

	var msgs []SessionMessage
	for rows.Next() {
		var m SessionMessage
		var createdAt string
		if err := rows.Scan(&m.ID, &m.ChatID, &m.UserID, &m.Role, &m.Content, &m.ToolCalls, &m.ToolCallID, &createdAt); err != nil {
			return nil, err
		}
		m.CreatedAt, _ = time.Parse("2006-01-02 15:04:05", createdAt)
		msgs = append(msgs, m)
	}
	return msgs, rows.Err()
}

// ActiveUserIDs returns DISTINCT user_id values for users we know about.
// Sources merged so we discover users even on first run (when session_messages
// rows still have '' user_ids from a pre-migration install):
//   1. session_messages.user_id (modern path, time-bounded by `since`)
//   2. user_memory.user_id (any user the bot has remembered something for)
//   3. tasks.user_id (any user with a cron/remind task)
// Empty strings are filtered out.
func (s *Store) ActiveUserIDs(since time.Time) ([]string, error) {
	rows, err := s.db.Query(
		`SELECT DISTINCT user_id FROM session_messages
		   WHERE user_id != '' AND created_at >= ?
		 UNION
		 SELECT DISTINCT user_id FROM user_memory
		   WHERE user_id != ''
		 UNION
		 SELECT DISTINCT user_id FROM tasks
		   WHERE user_id != ''`,
		since.UTC().Format("2006-01-02 15:04:05"),
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var out []string
	for rows.Next() {
		var uid string
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		out = append(out, uid)
	}
	return out, rows.Err()
}

// ListTasksByType returns all tasks of the given type, regardless of chat or
// user. Used by the evolution bootstrap to find existing system tasks.
func (s *Store) ListTasksByType(taskType string) ([]*Task, error) {
	rows, err := s.db.Query(
		`SELECT id, type, chat_id, user_id, description, schedule, next_run, one_shot, created_at
		   FROM tasks WHERE type = ? ORDER BY id`,
		taskType,
	)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	return scanTasks(rows)
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

// CreateKBDocumentWithChunks atomically inserts a document and all of its
// chunks inside a single transaction. Either everything is persisted or
// nothing — we never leave a document without its chunks, or chunks without
// their parent document. `chunks` and `embeddings` must be the same length.
func (s *Store) CreateKBDocumentWithChunks(chatID, title, content string, chunks []string, embeddings [][]byte) (int64, error) {
	if len(chunks) != len(embeddings) {
		return 0, fmt.Errorf("chunks/embeddings length mismatch: %d vs %d", len(chunks), len(embeddings))
	}

	tx, err := s.db.Begin()
	if err != nil {
		return 0, err
	}
	defer tx.Rollback()

	res, err := tx.Exec(
		`INSERT INTO kb_documents (chat_id, title, content) VALUES (?, ?, ?)`,
		chatID, title, content,
	)
	if err != nil {
		return 0, err
	}
	docID, err := res.LastInsertId()
	if err != nil {
		return 0, err
	}

	for i, chunk := range chunks {
		if _, err := tx.Exec(
			`INSERT INTO kb_chunks (document_id, chat_id, content, embedding) VALUES (?, ?, ?, ?)`,
			docID, chatID, chunk, embeddings[i],
		); err != nil {
			return 0, err
		}
	}

	if err := tx.Commit(); err != nil {
		return 0, err
	}
	return docID, nil
}

// ReplaceKBChunks atomically replaces all chunks of an existing document with
// a fresh set. Used by the re-embed flow after switching embedding models.
// The parent kb_documents row is untouched.
func (s *Store) ReplaceKBChunks(docID int64, chatID string, chunks []string, embeddings [][]byte) error {
	if len(chunks) != len(embeddings) {
		return fmt.Errorf("chunks/embeddings length mismatch: %d vs %d", len(chunks), len(embeddings))
	}

	tx, err := s.db.Begin()
	if err != nil {
		return err
	}
	defer tx.Rollback()

	if _, err := tx.Exec(`DELETE FROM kb_chunks WHERE document_id = ? AND chat_id = ?`, docID, chatID); err != nil {
		return err
	}
	for i, chunk := range chunks {
		if _, err := tx.Exec(
			`INSERT INTO kb_chunks (document_id, chat_id, content, embedding) VALUES (?, ?, ?, ?)`,
			docID, chatID, chunk, embeddings[i],
		); err != nil {
			return err
		}
	}
	return tx.Commit()
}

// SampleKBChunkDimension returns the byte-length of any chunk's embedding blob
// (i.e. dimension * 4 for float32). Used to detect dimension mismatches after
// an embedding-model switch. Returns 0 if there are no chunks.
func (s *Store) SampleKBChunkDimension() (int, error) {
	var length int
	err := s.db.QueryRow(`SELECT LENGTH(embedding) FROM kb_chunks WHERE embedding IS NOT NULL LIMIT 1`).Scan(&length)
	if err == sql.ErrNoRows {
		return 0, nil
	}
	if err != nil {
		return 0, err
	}
	return length / 4, nil
}

// CountKBDocuments returns the total number of KB documents and distinct chat_ids.
func (s *Store) CountKBDocuments() (docs int, chunks int, chats int, err error) {
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM kb_documents`).Scan(&docs); err != nil {
		return
	}
	if err = s.db.QueryRow(`SELECT COUNT(*) FROM kb_chunks`).Scan(&chunks); err != nil {
		return
	}
	err = s.db.QueryRow(`SELECT COUNT(DISTINCT chat_id) FROM kb_documents`).Scan(&chats)
	return
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

func (s *Store) GetKBDocument(chatID string, docID int64) (*KBDocument, error) {
	var d KBDocument
	err := s.db.QueryRow(
		`SELECT id, chat_id, title, content, created_at FROM kb_documents WHERE id = ? AND chat_id = ?`,
		docID, chatID,
	).Scan(&d.ID, &d.ChatID, &d.Title, &d.Content, &d.CreatedAt)
	if err != nil {
		return nil, err
	}
	return &d, nil
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

// --- Evolution Runs ---

type EvolutionRun struct {
	ID         int64
	UserID     string
	StartedAt  time.Time
	FinishedAt time.Time
	Summary    string
	Status     string
}

// BeginEvolutionRun records the start of an auto-evolution job and returns
// the run id used to finish it later.
func (s *Store) BeginEvolutionRun(userID string) (int64, error) {
	res, err := s.db.Exec(
		`INSERT INTO evolution_runs (user_id, status) VALUES (?, 'running')`,
		userID,
	)
	if err != nil {
		return 0, err
	}
	return res.LastInsertId()
}

// FinishEvolutionRun stamps the finish time, status, and JSON summary on a run.
func (s *Store) FinishEvolutionRun(id int64, status, summary string) error {
	_, err := s.db.Exec(
		`UPDATE evolution_runs SET finished_at = CURRENT_TIMESTAMP, status = ?, summary = ? WHERE id = ?`,
		status, summary, id,
	)
	return err
}

// LastEvolutionRun returns the most recent evolution run for userID, or nil
// when no run exists yet (sql.ErrNoRows is treated as "no run").
func (s *Store) LastEvolutionRun(userID string) (*EvolutionRun, error) {
	row := s.db.QueryRow(
		`SELECT id, user_id, started_at, COALESCE(finished_at, ''), COALESCE(summary, ''), status
		   FROM evolution_runs WHERE user_id = ? ORDER BY id DESC LIMIT 1`,
		userID,
	)
	var r EvolutionRun
	var startedAt, finishedAt string
	if err := row.Scan(&r.ID, &r.UserID, &startedAt, &finishedAt, &r.Summary, &r.Status); err != nil {
		if err == sql.ErrNoRows {
			return nil, nil
		}
		return nil, err
	}
	r.StartedAt, _ = time.Parse("2006-01-02 15:04:05", startedAt)
	if finishedAt != "" {
		r.FinishedAt, _ = time.Parse("2006-01-02 15:04:05", finishedAt)
	}
	return &r, nil
}
