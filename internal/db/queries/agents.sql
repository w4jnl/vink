-- name: CreateAgent :one
-- tenancy: org
INSERT INTO agents (id, org_id, name, token_hash, token_prefix, labels, version, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetAgent :one
-- tenancy: org
SELECT * FROM agents WHERE org_id = ? AND id = ?;

-- name: GetAgentByName :one
-- tenancy: org
SELECT * FROM agents WHERE org_id = ? AND name = ?;

-- name: ListAgents :many
-- tenancy: org
SELECT * FROM agents WHERE org_id = ? ORDER BY name;

-- name: CountAgents :one
-- tenancy: org
SELECT COUNT(*) FROM agents WHERE org_id = ?;

-- name: UpdateAgentLabels :one
-- tenancy: org
UPDATE agents SET labels = ? WHERE org_id = ? AND id = ? RETURNING *;

-- name: DeleteAgent :execrows
-- tenancy: org
DELETE FROM agents WHERE org_id = ? AND id = ?;

-- name: ListAgentsByPrefix :many
-- tenancy: root (bearer lookup establishes the org)
SELECT * FROM agents WHERE token_prefix = ?;

-- name: TouchAgent :exec
-- tenancy: root (the gateway acts for a verified agent)
UPDATE agents SET last_seen_at = ?, last_addr = ?, version = ? WHERE id = ?;
