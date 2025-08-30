-- name: GetMessage :one
SELECT *
FROM messages
WHERE id = ? LIMIT 1;

-- name: ListMessagesBySession :many
SELECT *
FROM messages
WHERE session_id = ?
ORDER BY created_at ASC, id ASC;

-- name: CreateMessage :one
INSERT INTO messages (
    id,
    session_id,
    role,
    parts,
    model,
    provider,
    created_at,
    updated_at
) VALUES (
    ?, ?, ?, ?, ?, ?, CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER), CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
)
RETURNING *;

-- name: UpdateMessage :exec
UPDATE messages
SET
    parts = ?,
    finished_at = ?,
    updated_at = CAST((julianday('now') - 2440587.5) * 86400000000 AS INTEGER)
WHERE id = ?;


-- name: DeleteMessage :exec
DELETE FROM messages
WHERE id = ?;

-- name: DeleteSessionMessages :exec
DELETE FROM messages
WHERE session_id = ?;

-- New delta queries for in-process UI reconciliation
-- name: ListSessionMessageChanges :many
SELECT *
FROM messages
WHERE session_id = sqlc.arg(session_id)
  AND (
    updated_at > sqlc.arg(updated_at)
    OR (updated_at = sqlc.arg(updated_at) AND id > sqlc.arg(id))
  )
ORDER BY updated_at ASC, id ASC
LIMIT sqlc.arg(limit);

-- name: ListSessionToolMessageChanges :many
SELECT *
FROM messages
WHERE session_id = sqlc.arg(session_id)
  AND role = 'tool'
  AND (
    created_at > sqlc.arg(created_at)
    OR (created_at = sqlc.arg(created_at) AND id > sqlc.arg(id))
  )
ORDER BY created_at ASC, id ASC
LIMIT sqlc.arg(limit);
