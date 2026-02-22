-- name: InsertMessage :execresult
INSERT INTO messages (session_id, role, content, created_at)
VALUES (?, ?, ?, CURRENT_TIMESTAMP);

-- name: ListMessagesBySession :many
SELECT id, session_id, role, content, created_at
FROM messages
WHERE session_id = ?
ORDER BY id DESC
LIMIT ?;
