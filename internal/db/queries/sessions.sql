-- name: CreateSession :exec
INSERT INTO sessions (id, user_id, csrf, created_at, expires_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetSession :one
SELECT * FROM sessions WHERE id = ? AND expires_at > ?;

-- name: TouchSession :exec
UPDATE sessions SET expires_at = ? WHERE id = ?;

-- name: SetSessionProject :exec
UPDATE sessions SET last_project_id = ? WHERE id = ?;

-- name: DeleteSession :exec
DELETE FROM sessions WHERE id = ?;

-- name: DeleteUserSessions :exec
DELETE FROM sessions WHERE user_id = ?;

-- name: DeleteExpiredSessions :execrows
DELETE FROM sessions WHERE expires_at <= ?;
