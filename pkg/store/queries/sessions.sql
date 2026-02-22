-- name: ListSessions :many
SELECT id, created_at, updated_at, last_message_at
FROM sessions
ORDER BY last_message_at DESC
LIMIT ?;

-- name: UpsertSession :exec
INSERT INTO sessions (id, created_at, updated_at, last_message_at)
VALUES (?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(id) DO UPDATE SET updated_at = CURRENT_TIMESTAMP;
