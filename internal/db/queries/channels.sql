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

-- name: DeleteChannel :execrows
DELETE FROM channels WHERE project_id = ? AND id = ?;
