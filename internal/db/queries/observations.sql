-- name: InsertObservation :exec
INSERT INTO observations (id, monitor_id, project_id, at, source, signal, ok, latency_ms, exit_code, run_id, duration_ms, remote_addr, user_agent, body_ref, detail)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: GetObservation :one
SELECT * FROM observations WHERE project_id = ? AND id = ?;

-- name: ListObservations :many
SELECT * FROM observations
WHERE project_id = sqlc.arg(project_id) AND monitor_id = sqlc.arg(monitor_id)
  AND at >= sqlc.arg(since) AND at <= sqlc.arg(until)
  AND (at < sqlc.arg(cursor_at) OR (at = sqlc.arg(cursor_at) AND id < sqlc.arg(cursor_id)))
ORDER BY at DESC, id DESC
LIMIT sqlc.arg(page_size);

-- name: ListObservationsSince :many
SELECT * FROM observations
WHERE project_id = ? AND monitor_id = ? AND at >= ?
ORDER BY at, id;

-- name: LastObservation :one
SELECT * FROM observations
WHERE project_id = ? AND monitor_id = ?
ORDER BY at DESC, id DESC
LIMIT 1;

-- name: DeleteObservationsBefore :execrows
-- tenancy: root (retention job)
DELETE FROM observations WHERE at < ?;
