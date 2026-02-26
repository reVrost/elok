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

	storesqlc "github.com/revrost/elok/pkg/store/sqlc"
	"github.com/revrost/elok/pkg/tenantctx"
	_ "modernc.org/sqlite"
)

//go:embed migrations/*.sql
var migrationFS embed.FS

type Store struct {
	db      *sql.DB
	queries *storesqlc.Queries
}

type ChatStore interface {
	AppendMessage(ctx context.Context, tenantID, sessionID, role, content string) (int64, error)
	ListSessions(ctx context.Context, tenantID string, limit int) ([]Session, error)
	ListMessages(ctx context.Context, tenantID, sessionID string, limit int) ([]Message, error)
}

type RuntimeConfigStore interface {
	GetRuntimeLLMConfig(ctx context.Context, tenantID string) (RuntimeLLMConfig, error)
	UpsertRuntimeLLMConfig(ctx context.Context, tenantID string, cfg RuntimeLLMConfig) error
}

type Session struct {
	TenantID      string    `json:"tenant_id,omitempty"`
	ID            string    `json:"id"`
	CreatedAt     time.Time `json:"created_at"`
	UpdatedAt     time.Time `json:"updated_at"`
	LastMessageAt time.Time `json:"last_message_at"`
}

type Message struct {
	ID        int64     `json:"id"`
	TenantID  string    `json:"tenant_id,omitempty"`
	SessionID string    `json:"session_id"`
	Role      string    `json:"role"`
	Content   string    `json:"content"`
	CreatedAt time.Time `json:"created_at"`
}

type RuntimeLLMConfig struct {
	TenantID         string    `json:"tenant_id,omitempty"`
	Provider         string    `json:"provider,omitempty"`
	Model            string    `json:"model,omitempty"`
	OpenRouterAPIKey string    `json:"openrouter_api_key,omitempty"`
	UpdatedAt        time.Time `json:"updated_at,omitempty"`
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
	return &Store{
		db:      db,
		queries: storesqlc.New(db),
	}, nil
}

func (s *Store) Close() error {
	if s == nil || s.db == nil {
		return nil
	}
	return s.db.Close()
}

func (s *Store) Migrate(ctx context.Context) error {
	if err := s.ensureMigrationsTable(ctx); err != nil {
		return err
	}
	applied, err := s.appliedMigrations(ctx)
	if err != nil {
		return err
	}

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
		if applied[name] {
			continue
		}
		data, err := migrationFS.ReadFile(filepath.Join("migrations", name))
		if err != nil {
			return fmt.Errorf("read migration %s: %w", name, err)
		}
		tx, err := s.db.BeginTx(ctx, nil)
		if err != nil {
			return fmt.Errorf("begin migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, string(data)); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("execute migration %s: %w", name, err)
		}
		if _, err := tx.ExecContext(ctx, `
INSERT INTO schema_migrations (name, applied_at)
VALUES (?, CURRENT_TIMESTAMP)
`, name); err != nil {
			_ = tx.Rollback()
			return fmt.Errorf("record migration %s: %w", name, err)
		}
		if err := tx.Commit(); err != nil {
			return fmt.Errorf("commit migration %s: %w", name, err)
		}
	}
	return nil
}

func (s *Store) ensureMigrationsTable(ctx context.Context) error {
	_, err := s.db.ExecContext(ctx, `
CREATE TABLE IF NOT EXISTS schema_migrations (
  name TEXT PRIMARY KEY,
  applied_at DATETIME NOT NULL DEFAULT CURRENT_TIMESTAMP
)
`)
	if err != nil {
		return fmt.Errorf("ensure schema_migrations table: %w", err)
	}
	return nil
}

func (s *Store) appliedMigrations(ctx context.Context) (map[string]bool, error) {
	rows, err := s.db.QueryContext(ctx, `SELECT name FROM schema_migrations`)
	if err != nil {
		return nil, fmt.Errorf("query applied migrations: %w", err)
	}
	defer rows.Close()

	applied := make(map[string]bool)
	for rows.Next() {
		var name string
		if err := rows.Scan(&name); err != nil {
			return nil, fmt.Errorf("scan applied migration: %w", err)
		}
		applied[name] = true
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate applied migrations: %w", err)
	}
	return applied, nil
}

func (s *Store) UpsertSession(ctx context.Context, tenantID, sessionID string) error {
	tenantID = tenantctx.Normalize(tenantID)
	if strings.TrimSpace(sessionID) == "" {
		return fmt.Errorf("session id is required")
	}
	_, err := s.db.ExecContext(ctx, `
INSERT INTO sessions (tenant_id, id, created_at, updated_at, last_message_at)
VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(tenant_id, id) DO UPDATE SET updated_at = CURRENT_TIMESTAMP
`, tenantID, sessionID)
	if err != nil {
		return fmt.Errorf("upsert session: %w", err)
	}
	return nil
}

func (s *Store) AppendMessage(ctx context.Context, tenantID, sessionID, role, content string) (int64, error) {
	tenantID = tenantctx.Normalize(tenantID)
	if err := s.UpsertSession(ctx, tenantID, sessionID); err != nil {
		return 0, err
	}
	res, err := s.db.ExecContext(ctx, `
INSERT INTO messages (tenant_id, session_id, role, content, created_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP)
`, tenantID, sessionID, role, content)
	if err != nil {
		return 0, fmt.Errorf("insert message: %w", err)
	}
	_, err = s.db.ExecContext(ctx, `
UPDATE sessions
SET updated_at = CURRENT_TIMESTAMP, last_message_at = CURRENT_TIMESTAMP
WHERE tenant_id = ? AND id = ?
`, tenantID, sessionID)
	if err != nil {
		return 0, fmt.Errorf("update session timestamps: %w", err)
	}
	id, err := res.LastInsertId()
	if err != nil {
		return 0, fmt.Errorf("last insert id: %w", err)
	}
	return id, nil
}

func (s *Store) ListSessions(ctx context.Context, tenantID string, limit int) ([]Session, error) {
	tenantID = tenantctx.Normalize(tenantID)
	if limit <= 0 {
		limit = 50
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT tenant_id, id, created_at, updated_at, last_message_at
FROM sessions
WHERE tenant_id = ?
ORDER BY last_message_at DESC
LIMIT ?
`, tenantID, limit)
	if err != nil {
		return nil, fmt.Errorf("list sessions: %w", err)
	}
	defer rows.Close()

	out := make([]Session, 0)
	for rows.Next() {
		var session Session
		if err := rows.Scan(&session.TenantID, &session.ID, &session.CreatedAt, &session.UpdatedAt, &session.LastMessageAt); err != nil {
			return nil, fmt.Errorf("scan session: %w", err)
		}
		out = append(out, session)
	}
	if err := rows.Err(); err != nil {
		return nil, fmt.Errorf("iterate sessions: %w", err)
	}
	return out, nil
}

func (s *Store) ListMessages(ctx context.Context, tenantID, sessionID string, limit int) ([]Message, error) {
	tenantID = tenantctx.Normalize(tenantID)
	if limit <= 0 {
		limit = 100
	}
	rows, err := s.db.QueryContext(ctx, `
SELECT id, tenant_id, session_id, role, content, created_at
FROM messages
WHERE tenant_id = ? AND session_id = ?
ORDER BY id DESC
LIMIT ?
`, tenantID, sessionID, limit)
	if err != nil {
		return nil, fmt.Errorf("list messages: %w", err)
	}
	defer rows.Close()

	reversed := make([]Message, 0)
	for rows.Next() {
		var msg Message
		if err := rows.Scan(&msg.ID, &msg.TenantID, &msg.SessionID, &msg.Role, &msg.Content, &msg.CreatedAt); err != nil {
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

func (s *Store) GetRuntimeLLMConfig(ctx context.Context, tenantID string) (RuntimeLLMConfig, error) {
	tenantID = tenantctx.Normalize(tenantID)
	cfg, err := s.queries.GetRuntimeLLMConfig(ctx, tenantID)
	if err != nil {
		if err == sql.ErrNoRows {
			return RuntimeLLMConfig{TenantID: tenantID}, nil
		}
		return RuntimeLLMConfig{}, fmt.Errorf("get runtime llm config: %w", err)
	}
	return RuntimeLLMConfig{
		TenantID:         cfg.TenantID,
		Provider:         cfg.Provider,
		Model:            cfg.Model,
		OpenRouterAPIKey: cfg.OpenrouterApiKey,
		UpdatedAt:        cfg.UpdatedAt,
	}, nil
}

func (s *Store) UpsertRuntimeLLMConfig(ctx context.Context, tenantID string, cfg RuntimeLLMConfig) error {
	tenantID = tenantctx.Normalize(tenantID)
	err := s.queries.UpsertRuntimeLLMConfig(ctx, storesqlc.UpsertRuntimeLLMConfigParams{
		TenantID:         tenantID,
		Provider:         strings.TrimSpace(cfg.Provider),
		Model:            strings.TrimSpace(cfg.Model),
		OpenrouterApiKey: strings.TrimSpace(cfg.OpenRouterAPIKey),
	})
	if err != nil {
		return fmt.Errorf("upsert runtime llm config: %w", err)
	}
	return nil
}
