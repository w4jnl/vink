-- name: CreateProject :one
INSERT INTO projects (id, org_id, slug, name, timezone, ping_key, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetProject :one
SELECT * FROM projects WHERE org_id = ? AND id = ?;

-- name: GetProjectBySlug :one
SELECT * FROM projects WHERE org_id = ? AND slug = ?;

-- name: GetProjectByID :one
-- tenancy: root (auth establishes the scope from an API key's project id)
SELECT * FROM projects WHERE id = ?;

-- name: GetProjectByPingKey :one
-- tenancy: root (ping ingress resolves the project from the key)
SELECT * FROM projects
WHERE ping_key = sqlc.arg(key) OR (ping_key_prev = sqlc.arg(key) AND ping_key_prev_until > sqlc.arg(now));

-- name: ListProjects :many
SELECT * FROM projects WHERE org_id = ? ORDER BY slug;

-- name: ListProjectsForUser :many
-- tenancy: root (the user's memberships are the scope)
SELECT sqlc.embed(p), o.slug AS org_slug, o.name AS org_name, m.role
FROM projects p
JOIN orgs o ON o.id = p.org_id
JOIN memberships m ON m.org_id = p.org_id
WHERE m.user_id = ?
ORDER BY o.slug, p.slug;

-- name: ListAllProjects :many
-- tenancy: root (instance admin)
SELECT sqlc.embed(p), o.slug AS org_slug, o.name AS org_name
FROM projects p JOIN orgs o ON o.id = p.org_id
ORDER BY o.slug, p.slug;

-- name: CountProjects :one
SELECT COUNT(*) FROM projects WHERE org_id = ?;

-- name: UpdateProject :exec
UPDATE projects SET name = ?, timezone = ? WHERE org_id = ? AND id = ?;

-- name: RotatePingKey :exec
UPDATE projects
SET ping_key = ?, ping_key_prev = ping_key, ping_key_prev_until = ?
WHERE org_id = ? AND id = ?;

-- name: DeleteProject :execrows
DELETE FROM projects WHERE org_id = ? AND id = ?;
