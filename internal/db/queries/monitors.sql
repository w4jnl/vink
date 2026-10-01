-- name: CreateMonitor :one
INSERT INTO monitors (id, project_id, org_id, slug, name, kind, spec, tags, state, state_since, base_at, next_due_at, created_at, updated_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
RETURNING *;

-- name: GetMonitor :one
SELECT * FROM monitors WHERE project_id = ? AND id = ?;

-- name: GetMonitorBySlug :one
SELECT * FROM monitors WHERE project_id = ? AND slug = ?;

-- name: ListMonitors :many
SELECT * FROM monitors WHERE project_id = ? ORDER BY name, id;

-- name: CountMonitorsByState :many
SELECT state, COUNT(*) AS n FROM monitors WHERE project_id = ? GROUP BY state;

-- name: CountMonitorsInOrg :one
-- tenancy: org (quota check)
SELECT COUNT(*) FROM monitors WHERE org_id = ?;

-- name: UpdateMonitor :one
UPDATE monitors
SET name = ?, spec = ?, tags = ?, next_due_at = ?, updated_at = ?
WHERE project_id = ? AND id = ?
RETURNING *;

-- name: UpdateMonitorState :exec
UPDATE monitors
SET state = ?, state_since = ?, base_at = ?, last_obs_at = ?, last_ok_at = ?, next_due_at = ?,
    fail_streak = ?, ok_streak = ?, run_started_at = ?, run_id = ?, updated_at = ?
WHERE project_id = ? AND id = ?;

-- name: SetMonitorPaused :exec
UPDATE monitors
SET paused = ?, state = ?, state_since = ?, base_at = ?, next_due_at = ?,
    fail_streak = 0, ok_streak = 0, run_started_at = NULL, run_id = NULL, updated_at = ?
WHERE project_id = ? AND id = ?;

-- name: DeleteMonitor :execrows
DELETE FROM monitors WHERE project_id = ? AND id = ?;

-- name: ListDueMonitors :many
-- tenancy: root (scheduler)
SELECT * FROM monitors
WHERE kind = 'heartbeat' AND paused = 0 AND next_due_at IS NOT NULL AND next_due_at <= ?
ORDER BY next_due_at
LIMIT ?;

-- name: NextDueAt :one
-- tenancy: root (scheduler)
SELECT CAST(COALESCE(MIN(next_due_at), 0) AS INTEGER) AS next_due_at
FROM monitors WHERE kind = 'heartbeat' AND paused = 0 AND next_due_at IS NOT NULL;

-- name: ListDueChecks :many
-- tenancy: root (checker pool; remote checks belong to an agent)
SELECT id FROM monitors
WHERE kind <> 'heartbeat' AND paused = 0 AND next_due_at IS NOT NULL AND next_due_at <= ?
  AND COALESCE(json_extract(spec, '$.location'), '') = ''
ORDER BY next_due_at
LIMIT ?;

-- name: NextCheckDueAt :one
-- tenancy: root (checker pool)
SELECT CAST(COALESCE(MIN(next_due_at), 0) AS INTEGER) AS next_due_at
FROM monitors WHERE kind <> 'heartbeat' AND paused = 0 AND next_due_at IS NOT NULL
  AND COALESCE(json_extract(spec, '$.location'), '') = '';

-- name: ListRemoteMonitors :many
-- tenancy: root (agent gateway assigns per org)
SELECT * FROM monitors
WHERE org_id = ? AND kind <> 'heartbeat' AND COALESCE(json_extract(spec, '$.location'), '') <> ''
ORDER BY id;

-- name: ListAgentMonitors :many
-- tenancy: root (agent gateway, for a verified agent)
SELECT * FROM monitors WHERE agent_id = ? ORDER BY id;

-- name: ListUnassignedRemoteMonitors :many
-- tenancy: root (offline sweep)
SELECT * FROM monitors
WHERE agent_id IS NULL AND kind <> 'heartbeat' AND paused = 0 AND state IN ('new', 'up')
  AND COALESCE(json_extract(spec, '$.location'), '') <> '' AND updated_at <= ?;

-- name: SetMonitorAgent :exec
-- tenancy: root (agent gateway)
UPDATE monitors SET agent_id = ? WHERE id = ?;

-- name: ClearAgentMonitors :execrows
-- tenancy: root (agent gateway)
UPDATE monitors SET agent_id = NULL WHERE agent_id = ?;

-- name: ListRunningMonitors :many
-- tenancy: root (scheduler, max_runtime)
SELECT * FROM monitors WHERE paused = 0 AND run_started_at IS NOT NULL AND run_started_at <= ?;

-- name: GetMonitorByID :one
-- tenancy: root (scheduler reload after a bus event)
SELECT * FROM monitors WHERE id = ?;

-- name: ListMonitorsForMetrics :many
-- tenancy: root (metrics endpoint, instance-wide)
SELECT m.kind, m.state, m.paused, o.slug AS org_slug, p.slug AS project_slug, COUNT(*) AS n
FROM monitors m JOIN projects p ON p.id = m.project_id JOIN orgs o ON o.id = p.org_id
GROUP BY o.slug, p.slug, m.kind, m.state, m.paused
ORDER BY o.slug, p.slug, m.kind, m.state;

-- name: CountOpenIncidentsByProject :many
-- tenancy: root (metrics endpoint, instance-wide)
SELECT o.slug AS org_slug, p.slug AS project_slug, COUNT(*) AS n
FROM incidents i JOIN projects p ON p.id = i.project_id JOIN orgs o ON o.id = p.org_id
WHERE i.resolved_at IS NULL
GROUP BY o.slug, p.slug;
