-- name: CreateMaintenance :one
INSERT INTO maintenance (id, project_id, name, match_tags, starts_at, ends_at, rrule, from_time, to_time, timezone, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetMaintenance :one
SELECT * FROM maintenance WHERE project_id = ? AND id = ?;

-- name: ListMaintenance :many
SELECT * FROM maintenance WHERE project_id = ? ORDER BY created_at, id;

-- name: UpdateMaintenance :one
UPDATE maintenance
SET name = ?, match_tags = ?, starts_at = ?, ends_at = ?, rrule = ?, from_time = ?, to_time = ?, timezone = ?, ended_until = ?, updated_at = ?
WHERE project_id = ? AND id = ?
RETURNING *;

-- name: DeleteMaintenance :execrows
DELETE FROM maintenance WHERE project_id = ? AND id = ?;
