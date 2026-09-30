-- name: CreateStatusPage :one
INSERT INTO status_pages (id, project_id, slug, title, match_tags, public, password_hash, custom_domain, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetStatusPage :one
SELECT * FROM status_pages WHERE project_id = ? AND slug = ?;

-- name: ListStatusPages :many
SELECT * FROM status_pages WHERE project_id = ? ORDER BY title, id;

-- name: UpdateStatusPage :one
UPDATE status_pages
SET slug = ?, title = ?, match_tags = ?, public = ?, password_hash = ?, custom_domain = ?, updated_at = ?
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
