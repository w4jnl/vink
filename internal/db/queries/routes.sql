-- name: CreateRoute :one
INSERT INTO routes (id, project_id, match_tags, channel_id, on_states, repeat_every_s, priority, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetRoute :one
SELECT * FROM routes WHERE project_id = ? AND id = ?;

-- name: ListRoutes :many
SELECT r.*, c.name AS channel_name, c.kind AS channel_kind, c.enabled AS channel_enabled
FROM routes r JOIN channels c ON c.id = r.channel_id
WHERE r.project_id = ?
ORDER BY r.priority DESC, r.created_at, r.id;

-- name: UpdateRoute :one
UPDATE routes
SET match_tags = ?, channel_id = ?, on_states = ?, repeat_every_s = ?, priority = ?, updated_at = ?
WHERE project_id = ? AND id = ?
RETURNING *;

-- name: DeleteRoute :execrows
DELETE FROM routes WHERE project_id = ? AND id = ?;
