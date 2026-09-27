-- name: CreateUser :one
INSERT INTO users (id, subject, email, display_name, password_hash, is_instance_admin, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetUser :one
SELECT * FROM users WHERE id = ?;

-- name: GetUserBySubject :one
SELECT * FROM users WHERE subject = ?;

-- name: ListUsers :many
SELECT * FROM users ORDER BY subject;

-- name: CountUsers :one
SELECT COUNT(*) FROM users;

-- name: UpdateUserProfile :exec
UPDATE users SET email = ?, display_name = ? WHERE id = ?;

-- name: SetUserPassword :exec
UPDATE users SET password_hash = ? WHERE id = ?;

-- name: SetInstanceAdmin :execrows
UPDATE users SET is_instance_admin = ? WHERE id = ?;
