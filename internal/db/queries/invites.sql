-- name: CreateInvite :one
-- tenancy: org
INSERT INTO invites (id, org_id, role, note, token_hash, created_by, created_at, expires_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: ListInvitesForOrg :many
-- tenancy: org
SELECT i.*, u.subject AS created_by_subject, u.display_name AS created_by_name, j.subject AS used_by_subject
FROM invites i LEFT JOIN users u ON u.id = i.created_by LEFT JOIN users j ON j.id = i.used_by
WHERE i.org_id = ?
ORDER BY i.created_at DESC;

-- name: GetInvite :one
-- tenancy: org
SELECT * FROM invites WHERE org_id = ? AND id = ?;

-- name: GetInviteByHash :one
-- tenancy: root (the link's token establishes the org)
SELECT i.*, o.slug AS org_slug, o.name AS org_name, u.subject AS created_by_subject, u.display_name AS created_by_name
FROM invites i JOIN orgs o ON o.id = i.org_id LEFT JOIN users u ON u.id = i.created_by
WHERE i.token_hash = ?;

-- name: UseInvite :execrows
-- tenancy: root (the link's token establishes the org; the row must still be open)
UPDATE invites SET used_at = ?, used_by = ?
WHERE id = ? AND used_at IS NULL AND revoked_at IS NULL AND expires_at > ?;

-- name: RevokeInvite :execrows
-- tenancy: org
UPDATE invites SET revoked_at = ? WHERE org_id = ? AND id = ? AND used_at IS NULL AND revoked_at IS NULL;

-- name: DeleteInvite :execrows
-- tenancy: org
DELETE FROM invites WHERE org_id = ? AND id = ? AND used_at IS NULL;
