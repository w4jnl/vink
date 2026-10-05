-- name: CreateUser :one
INSERT INTO users (id, subject, email, display_name, password_hash, is_instance_admin, source, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
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

-- name: ListUsersWithSeen :many
-- tenancy: root (instance admin)
SELECT u.*, (SELECT MAX(s.last_seen_at) FROM sessions s WHERE s.user_id = u.id) AS last_seen_at
FROM users u ORDER BY u.subject;

-- name: SetUserSource :exec
UPDATE users SET source = ? WHERE id = ?;

-- name: SetUserDisabled :exec
-- tenancy: root (instance admin)
UPDATE users SET disabled_at = ?, disabled_by = ? WHERE id = ?;

-- name: ResetUserTOTP :exec
-- tenancy: root (instance admin, or the account itself)
UPDATE users SET totp_secret = NULL, totp_enabled_at = NULL, totp_last_step = 0 WHERE id = ?;

-- name: DeleteRecoveryCodes :exec
-- tenancy: root (follows the user)
DELETE FROM recovery_codes WHERE user_id = ?;

-- name: CreateResetToken :exec
-- tenancy: root (instance admin)
INSERT INTO reset_tokens (id, user_id, token_hash, created_by, created_at, expires_at) VALUES (?, ?, ?, ?, ?, ?);

-- name: GetResetTokenByHash :one
-- tenancy: root (the link's token establishes the user)
SELECT r.*, u.subject, u.display_name, c.subject AS created_by_subject, c.display_name AS created_by_name
FROM reset_tokens r JOIN users u ON u.id = r.user_id LEFT JOIN users c ON c.id = r.created_by
WHERE r.token_hash = ?;

-- name: UseResetToken :execrows
-- tenancy: root (the link's token establishes the user)
UPDATE reset_tokens SET used_at = ? WHERE id = ? AND used_at IS NULL AND expires_at > ?;

-- name: CountActiveInstanceAdmins :one
SELECT COUNT(*) FROM users WHERE is_instance_admin = 1 AND disabled_at IS NULL;

