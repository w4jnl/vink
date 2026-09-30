-- name: OpenIncident :one
INSERT INTO incidents (id, monitor_id, project_id, opened_at, open_event_id)
VALUES (?, ?, ?, ?, ?)
RETURNING *;

-- name: GetIncident :one
SELECT i.*, COALESCE(e.reason, '') AS reason
FROM incidents i LEFT JOIN events e ON e.id = i.open_event_id
WHERE i.project_id = ? AND i.id = ?;

-- name: GetOpenIncidentForMonitor :one
SELECT * FROM incidents
WHERE project_id = ? AND monitor_id = ? AND resolved_at IS NULL
ORDER BY opened_at DESC
LIMIT 1;

-- name: ResolveIncident :exec
UPDATE incidents SET resolved_at = ?, close_event_id = ?
WHERE project_id = ? AND id = ?;

-- name: AckIncident :execrows
UPDATE incidents SET acked_by = ?, acked_at = ?
WHERE project_id = ? AND id = ? AND acked_at IS NULL AND resolved_at IS NULL;

-- name: ListIncidents :many
SELECT i.*, m.slug AS monitor_slug, m.name AS monitor_name, m.tags AS monitor_tags, COALESCE(e.reason, '') AS reason
FROM incidents i JOIN monitors m ON m.id = i.monitor_id LEFT JOIN events e ON e.id = i.open_event_id
WHERE i.project_id = sqlc.arg(project_id) AND (i.resolved_at IS NULL OR i.resolved_at >= sqlc.arg(since))
ORDER BY (i.resolved_at IS NULL) DESC, i.opened_at DESC
LIMIT sqlc.arg(limit);

-- name: ListOpenIncidents :many
SELECT i.*, m.slug AS monitor_slug, m.name AS monitor_name, m.tags AS monitor_tags, COALESCE(e.reason, '') AS reason
FROM incidents i JOIN monitors m ON m.id = i.monitor_id LEFT JOIN events e ON e.id = i.open_event_id
WHERE i.project_id = ? AND i.resolved_at IS NULL
ORDER BY i.opened_at DESC;

-- name: CountOpenIncidents :one
SELECT COUNT(*) FROM incidents WHERE project_id = ? AND resolved_at IS NULL;

-- name: ListOpenUnackedIncidents :many
-- tenancy: root (dispatcher schedules repeat notifications)
SELECT i.*, m.slug AS monitor_slug, m.name AS monitor_name
FROM incidents i JOIN monitors m ON m.id = i.monitor_id
WHERE i.resolved_at IS NULL AND i.acked_at IS NULL AND m.paused = 0
ORDER BY i.opened_at;
