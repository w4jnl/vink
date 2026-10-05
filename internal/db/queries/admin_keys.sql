-- Instance admin API keys belong to no org or project: every query here
-- is instance-wide by design.

-- name: CreateAdminKey :one
-- tenancy: root
INSERT INTO admin_keys (id, name, prefix, hash, access, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAdminKeysByPrefix :many
-- tenancy: root (the verify path: a prefix may be shared, the hash decides)
SELECT * FROM admin_keys WHERE prefix = ? AND revoked_at IS NULL;

-- name: GetAdminKeyState :one
-- tenancy: root (re-checks a cached key on every use)
SELECT revoked_at, expires_at FROM admin_keys WHERE id = ?;

-- name: TouchAdminKey :exec
-- tenancy: root
UPDATE admin_keys SET last_used_at = ?, last_used_ip = ? WHERE id = ?;

-- name: ListAdminKeys :many
-- tenancy: root (keys not revoked, expired ones included)
SELECT * FROM admin_keys WHERE revoked_at IS NULL ORDER BY created_at DESC, id;

-- name: GetAdminKey :one
-- tenancy: root
SELECT * FROM admin_keys WHERE id = ?;

-- name: RevokeAdminKey :execrows
-- tenancy: root
UPDATE admin_keys SET revoked_at = ? WHERE id = ? AND revoked_at IS NULL;

-- name: ListAdminKeysCreatedBy :many
-- tenancy: root (the keys to revoke when their creator loses instance admin)
SELECT * FROM admin_keys WHERE created_by = ? AND revoked_at IS NULL;
