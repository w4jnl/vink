-- name: CreateChannel :one
INSERT INTO channels (id, project_id, org_id, name, kind, config, enabled, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetChannel :one
SELECT * FROM channels WHERE project_id = ? AND id = ?;

-- name: GetChannelByName :one
SELECT * FROM channels WHERE project_id = ? AND name = ?;

-- name: ListChannels :many
SELECT * FROM channels WHERE project_id = ? ORDER BY name;

-- name: UpdateChannel :one
UPDATE channels
SET name = ?, kind = ?, config = ?, enabled = ?, updated_at = ?
WHERE project_id = ? AND id = ?
RETURNING *;

-- name: SetChannelEnabled :one
UPDATE channels
SET enabled = ?, updated_at = ?
WHERE project_id = ? AND id = ?
RETURNING *;

-- name: DeleteChannel :execrows
DELETE FROM channels WHERE project_id = ? AND id = ?;

-- name: GetOrgChannel :one
-- tenancy: org (a channel of the org itself, project_id NULL)
SELECT * FROM channels WHERE org_id = ? AND project_id IS NULL AND id = ?;

-- name: ListOrgChannels :many
-- tenancy: org
SELECT * FROM channels WHERE org_id = ? AND project_id IS NULL ORDER BY name;

-- name: UpdateOrgChannel :one
-- tenancy: org
UPDATE channels
SET name = ?, kind = ?, config = ?, enabled = ?, updated_at = ?
WHERE org_id = ? AND project_id IS NULL AND id = ?
RETURNING *;

-- name: SetOrgChannelEnabled :one
-- tenancy: org
UPDATE channels
SET enabled = ?, updated_at = ?
WHERE org_id = ? AND project_id IS NULL AND id = ?
RETURNING *;

-- name: DeleteOrgChannel :execrows
-- tenancy: org
DELETE FROM channels WHERE org_id = ? AND project_id IS NULL AND id = ?;
