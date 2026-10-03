-- name: InsertEvent :exec
INSERT INTO events (id, monitor_id, project_id, at, from_state, to_state, reason, observation_id)
VALUES (?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetEvent :one
SELECT * FROM events WHERE project_id = ? AND id = ?;

-- name: ListEvents :many
SELECT * FROM events
WHERE project_id = ? AND monitor_id = ?
ORDER BY at DESC, id DESC
LIMIT ?;

-- name: ListEventsPage :many
SELECT * FROM events
WHERE project_id = sqlc.arg(project_id) AND monitor_id = sqlc.arg(monitor_id)
  AND at >= sqlc.arg(since) AND at <= sqlc.arg(until)
  AND (at < sqlc.arg(cursor_at) OR (at = sqlc.arg(cursor_at) AND id < sqlc.arg(cursor_id)))
ORDER BY at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: ListEventsSince :many
SELECT * FROM events
WHERE project_id = ? AND monitor_id = ? AND at >= ?
ORDER BY at, id;

-- name: ListProjectEvents :many
SELECT e.*, m.slug AS monitor_slug, m.name AS monitor_name
FROM events e JOIN monitors m ON m.id = e.monitor_id
WHERE e.project_id = ?
ORDER BY e.at DESC, e.id DESC
LIMIT ?;

-- name: ListProjectEventsSince :many
SELECT * FROM events
WHERE project_id = ? AND at >= ?
ORDER BY at, id;
