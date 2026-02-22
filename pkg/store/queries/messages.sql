-- name: InsertMessage :execresult
INSERT INTO messages (tenant_id, session_id, role, content, created_at)
VALUES (?, ?, ?, ?, CURRENT_TIMESTAMP);

-- name: ListMessagesBySession :many
SELECT id, tenant_id, session_id, role, content, created_at
FROM messages
WHERE tenant_id = ? AND session_id = ?
ORDER BY id DESC
LIMIT ?;
