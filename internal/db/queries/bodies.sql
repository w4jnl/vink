-- name: InsertBody :exec
INSERT INTO bodies (observation_id, project_id, content, content_type, created_at)
VALUES (?, ?, ?, ?, ?);

-- name: GetBody :one
SELECT * FROM bodies WHERE project_id = ? AND observation_id = ?;

-- name: DeleteBodiesBefore :execrows
-- tenancy: root (retention job)
DELETE FROM bodies WHERE created_at < ?;
