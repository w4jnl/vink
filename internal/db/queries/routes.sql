-- name: CreateRoute :one
INSERT INTO routes (id, project_id, match_tags, on_states, repeat_every_s, priority, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetRoute :one
SELECT * FROM routes WHERE project_id = ? AND id = ?;

-- name: ListRoutes :many
SELECT * FROM routes WHERE project_id = ? ORDER BY priority DESC, created_at, id;

-- name: ListRouteChannels :many
SELECT rc.route_id, c.id AS channel_id, c.name AS channel_name, c.kind AS channel_kind, c.enabled AS channel_enabled
FROM route_channels rc JOIN channels c ON c.id = rc.channel_id
WHERE rc.project_id = ?
ORDER BY rc.route_id, c.name;

-- name: InsertRouteChannel :exec
INSERT INTO route_channels (route_id, channel_id, project_id) VALUES (?, ?, ?);

-- name: DeleteRouteChannels :exec
DELETE FROM route_channels WHERE project_id = ? AND route_id = ?;

-- name: CountRoutesForChannel :one
SELECT COUNT(*) FROM route_channels WHERE project_id = ? AND channel_id = ?;

-- name: UpdateRoute :one
UPDATE routes
SET match_tags = ?, on_states = ?, repeat_every_s = ?, priority = ?, updated_at = ?
WHERE project_id = ? AND id = ?
RETURNING *;

-- name: DeleteRoute :execrows
DELETE FROM routes WHERE project_id = ? AND id = ?;

-- name: DeleteOrphanRoutes :execrows
DELETE FROM routes WHERE routes.project_id = ? AND NOT EXISTS (SELECT 1 FROM route_channels rc WHERE rc.route_id = routes.id);
