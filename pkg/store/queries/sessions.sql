-- name: ListSessions :many
SELECT tenant_id, id, created_at, updated_at, last_message_at
FROM sessions
WHERE tenant_id = ?
ORDER BY last_message_at DESC
LIMIT ?;

-- name: UpsertSession :exec
INSERT INTO sessions (tenant_id, id, created_at, updated_at, last_message_at)
VALUES (?, ?, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP, CURRENT_TIMESTAMP)
ON CONFLICT(tenant_id, id) DO UPDATE SET updated_at = CURRENT_TIMESTAMP;
