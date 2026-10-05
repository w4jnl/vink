-- name: CreateStatusPage :one
-- a project page has project_id; an org page has none (project_id NULL)
INSERT INTO status_pages (id, org_id, project_id, slug, title, match_tags, projects, group_by, incidents, public, password_hash, custom_domain, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetStatusPage :one
SELECT * FROM status_pages WHERE project_id = ? AND slug = ?;

-- name: ListStatusPages :many
SELECT * FROM status_pages WHERE project_id = ? ORDER BY title, id;

-- name: UpdateStatusPage :one
UPDATE status_pages
SET slug = ?, title = ?, match_tags = ?, incidents = ?, public = ?, password_hash = ?, custom_domain = ?, updated_at = ?
WHERE project_id = ? AND id = ?
RETURNING *;

-- name: DeleteStatusPage :execrows
DELETE FROM status_pages WHERE project_id = ? AND slug = ?;

-- name: GetStatusPageBySlug :one
-- tenancy: root (public page: the slug is unique per instance)
SELECT * FROM status_pages WHERE slug = ?;

-- name: GetStatusPageByDomain :one
-- tenancy: root (public page served on its custom domain)
SELECT * FROM status_pages WHERE custom_domain = ?;

-- name: GetOrgStatusPage :one
-- tenancy: org (a page of the org itself, project_id NULL)
SELECT * FROM status_pages WHERE org_id = ? AND project_id IS NULL AND slug = ?;

-- name: ListOrgStatusPages :many
-- tenancy: org
SELECT * FROM status_pages WHERE org_id = ? AND project_id IS NULL ORDER BY title, id;

-- name: UpdateOrgStatusPage :one
-- tenancy: org
UPDATE status_pages
SET slug = ?, title = ?, match_tags = ?, projects = ?, group_by = ?, incidents = ?, public = ?, password_hash = ?, custom_domain = ?, updated_at = ?
WHERE org_id = ? AND project_id IS NULL AND id = ?
RETURNING *;

-- name: DeleteOrgStatusPage :execrows
-- tenancy: org
DELETE FROM status_pages WHERE org_id = ? AND project_id IS NULL AND slug = ?;
