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

func boolToInt(b bool) int {
	if b {
		return 1
	}
	return 0
}
