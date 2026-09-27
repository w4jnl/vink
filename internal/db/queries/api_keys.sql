-- name: CreateAPIKey :one
INSERT INTO api_keys (id, project_id, org_id, name, prefix, hash, access, created_by, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListAPIKeysByPrefix :many
-- tenancy: root (bearer lookup establishes the scope)
SELECT * FROM api_keys WHERE prefix = ? AND revoked_at IS NULL;

-- name: TouchAPIKey :exec
-- tenancy: root (called after the key was verified)
UPDATE api_keys SET last_used_at = ? WHERE id = ?;

-- name: ListAPIKeys :many
SELECT * FROM api_keys WHERE project_id = ? AND revoked_at IS NULL ORDER BY created_at;

-- name: GetAPIKey :one
SELECT * FROM api_keys WHERE project_id = ? AND id = ?;

-- name: RevokeAPIKey :execrows
UPDATE api_keys SET revoked_at = ? WHERE project_id = ? AND id = ? AND revoked_at IS NULL;
