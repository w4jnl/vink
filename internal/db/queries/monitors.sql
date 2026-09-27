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
WHERE paused = 0 AND next_due_at IS NOT NULL AND next_due_at <= ?
ORDER BY next_due_at
LIMIT ?;

-- name: NextDueAt :one
-- tenancy: root (scheduler)
SELECT CAST(COALESCE(MIN(next_due_at), 0) AS INTEGER) AS next_due_at
FROM monitors WHERE paused = 0 AND next_due_at IS NOT NULL;

-- name: ListRunningMonitors :many
-- tenancy: root (scheduler, max_runtime)
SELECT * FROM monitors WHERE paused = 0 AND run_started_at IS NOT NULL AND run_started_at <= ?;

-- name: GetMonitorByID :one
-- tenancy: root (scheduler reload after a bus event)
SELECT * FROM monitors WHERE id = ?;
