-- name: CreateOrg :one
INSERT INTO orgs (id, slug, name, created_at)
VALUES (?, ?, ?, ?)
RETURNING *;

-- name: GetOrg :one
SELECT * FROM orgs WHERE id = ?;

-- name: GetOrgBySlug :one
SELECT * FROM orgs WHERE slug = ?;

-- name: ListOrgs :many
SELECT * FROM orgs ORDER BY slug;

-- name: CountOrgs :one
SELECT COUNT(*) FROM orgs;

-- name: SetOrgQuotas :exec
UPDATE orgs SET quota_monitors = ?, quota_agents = ? WHERE id = ?;

-- name: DeleteOrg :execrows
DELETE FROM orgs WHERE id = ?;
