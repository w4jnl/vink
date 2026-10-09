-- name: CreateOrgRoute :one
INSERT INTO org_routes (id, org_id, projects, match_tags, on_states, repeat_every_s, priority, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetOrgRoute :one
SELECT * FROM org_routes WHERE org_id = ? AND id = ?;

-- name: ListOrgRoutes :many
SELECT * FROM org_routes WHERE org_id = ? ORDER BY priority DESC, created_at, id;

-- name: ListOrgRouteChannels :many
-- tenancy: org (org channels, project_id NULL)
SELECT rc.route_id, c.id AS channel_id, c.name AS channel_name, c.kind AS channel_kind, c.enabled AS channel_enabled
FROM org_route_channels rc JOIN channels c ON c.id = rc.channel_id
WHERE rc.org_id = ?
ORDER BY rc.route_id, c.name;

-- name: InsertOrgRouteChannel :exec
INSERT INTO org_route_channels (route_id, channel_id, org_id) VALUES (?, ?, ?);

-- name: DeleteOrgRouteChannels :exec
DELETE FROM org_route_channels WHERE org_id = ? AND route_id = ?;

-- name: CountOrgRoutesForChannel :one
SELECT COUNT(*) FROM org_route_channels WHERE org_id = ? AND channel_id = ?;

-- name: UpdateOrgRoute :one
UPDATE org_routes
SET projects = ?, match_tags = ?, on_states = ?, repeat_every_s = ?, priority = ?, updated_at = ?
WHERE org_id = ? AND id = ?
RETURNING *;

-- name: DeleteOrgRoute :execrows
DELETE FROM org_routes WHERE org_id = ? AND id = ?;

-- name: DeleteOrphanOrgRoutes :execrows
DELETE FROM org_routes WHERE org_routes.org_id = ? AND NOT EXISTS (SELECT 1 FROM org_route_channels rc WHERE rc.route_id = org_routes.id);
