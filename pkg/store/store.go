package store

import (
	"context"
	"database/sql"
	"embed"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	db *sql.DB
}

type Session struct {
	ID            string    `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastMessageAt time.Time `json:"last_message_at"`
}

type Message struct {
	ID        int64     `json:"id"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

func Open(path string) (*Store, error) {
	if path == "" {
		return nil, fmt.Errorf("db path is required")
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return nil, fmt.Errorf("mkdir db dir: %w", err)
	}
	db, err := sql.Open("sqlite", path)
	if err != nil {
		return nil, fmt.Errorf("open sqlite: %w", err)
	}
	if _, err := db.Exec("PRAGMA journal_mode=WAL;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set journal mode: %w", err)
	}
	if _, err := db.Exec("PRAGMA foreign_keys=ON;"); err != nil {
		_ = db.Close()
		return nil, fmt.Errorf("set foreign_keys: %w", err)
	}
	return &Store{db: db}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	entries, err := migrationFS.ReadDir("migrations")
	if err != nil {
		return fmt.Errorf("read migrations: %w", err)
	}
	names := make([]string, 0, len(entries))
	for _, entry := range entries {
		if entry.IsDir() || !strings.HasSuffix(entry.Name(), ".sql") {
			continue
		}
		names = append(names, entry.Name())
	}
	sort.Strings(names)
	for _, name := range names {
		data, err := migrationFS.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		if _, err := s.db.ExecContext(ctx, string(data)); err != nil {
			return fmt.Errorf("execute migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) UpsertSession(ctx context.Context, sessionID string) error {
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session id is required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (id, created_at, updated_at, last_message_at)
VALUES (?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET updated_at = CURRENT_TIMESTAMP
`, sessionID)
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	return nil
}

func (s *Store) AppendMessage(ctx context.Context, sessionID, role, content string) (int64, error) {
	if err := s.UpsertSession(ctx, sessionID); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO messages (session_id, role, content, created_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP)
`, sessionID, role, content)
	if err != nil {
		return 0, fmt.Errorf("insert message: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE sessions
SET updated_at = CURRENT_TIMESTAMP, last_message_at = CURRENT_TIMESTAMP
WHERE id = ?
`, sessionID)
	if err != nil {
		return 0, fmt.Errorf("update session timestamps: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return id, nil
}

func (s *Store) ListSessions(ctx context.Context, limit int) ([]Session, error) {
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, created_at, updated_at, last_message_at
FROM sessions
ORDER BY last_message_at DESC
LIMIT ?
`, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	out := make([]Session, 0)
	for rows.Next() {
		var session Session
		if err := rows.Scan(&session.ID, &session.CreatedAt, &session.UpdatedAt, &session.LastMessageAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return out, nil
}

func (s *Store) ListMessages(ctx context.Context, sessionID string, limit int) ([]Message, error) {
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, session_id, role, content, created_at
FROM messages
WHERE session_id = ?
ORDER BY id DESC
LIMIT ?
`, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	reversed := make([]Message, 0)
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.SessionID, &msg.Role, &msg.Content, &msg.CreatedAt); err != nil {
			return nil, fmt.Errorf("scan message: %w", err)
		}
		reversed = append(reversed, msg)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate messages: %w", err)
	}

	for i, j := 0, len(reversed)-1; i < j; i, j = i+1, j-1 {
		reversed[i], reversed[j] = reversed[j], reversed[i]
	}
	return reversed, nil
}
