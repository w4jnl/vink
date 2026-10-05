-- name: UpsertLocalMembership :exec
-- a role set in vink: it replaces whatever the user had in the org
INSERT INTO memberships (user_id, org_id, role, source, created_at)
VALUES (?, ?, ?, 'local', ?)
ON CONFLICT (user_id, org_id) DO UPDATE SET role = excluded.role, source = 'local';

-- name: UpsertDerivedMembership :exec
-- a role from a provider's groups (source header or oidc): it only ever
-- updates a row of the same source, never a role set in vink or one from
-- the other provider
INSERT INTO memberships (user_id, org_id, role, source, created_at)
VALUES (?, ?, ?, ?, ?)
ON CONFLICT (user_id, org_id) DO UPDATE SET role = excluded.role WHERE memberships.source = excluded.source;

-- name: SetMembershipRole :exec
-- changes the role and keeps where it came from
UPDATE memberships SET role = ? WHERE user_id = ? AND org_id = ?;

-- name: GetMembership :one
SELECT * FROM memberships WHERE user_id = ? AND org_id = ?;

-- name: ListMembershipsForUser :many
SELECT m.*, o.slug AS org_slug, o.name AS org_name
FROM memberships m JOIN orgs o ON o.id = m.org_id
WHERE m.user_id = ?
ORDER BY o.slug;

-- name: ListMembershipsForOrg :many
SELECT m.*, u.subject, u.email, u.display_name
FROM memberships m JOIN users u ON u.id = m.user_id
WHERE m.org_id = ?
ORDER BY u.subject;

-- name: DeleteMembership :execrows
DELETE FROM memberships WHERE user_id = ? AND org_id = ?;

-- name: DeleteMembershipsBySource :exec
DELETE FROM memberships WHERE user_id = ? AND source = ?;

-- name: DeleteHeaderMembershipsForUser :exec
DELETE FROM memberships WHERE user_id = ? AND source = 'header';

-- name: ListOrgMembers :many
-- tenancy: org
SELECT m.*, u.subject, u.email, u.display_name, u.source AS user_source, u.disabled_at,
  (SELECT MAX(s.last_seen_at) FROM sessions s WHERE s.user_id = m.user_id) AS last_seen_at
FROM memberships m JOIN users u ON u.id = m.user_id
WHERE m.org_id = ?
ORDER BY u.subject;

-- name: CountOwners :one
-- tenancy: org
SELECT COUNT(*) FROM memberships WHERE org_id = ? AND role = 'owner';

-- name: ListAllMemberships :many
-- tenancy: root (instance admin: every user's roles)
SELECT m.*, o.slug AS org_slug, o.name AS org_name
FROM memberships m JOIN orgs o ON o.id = m.org_id
ORDER BY o.slug;
