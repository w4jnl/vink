-- name: InsertDelivery :exec
INSERT INTO deliveries (id, event_id, channel_id, project_id, monitor_id, route_id, kind, repeat, attempt, next_attempt_at, created_at)
VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?);

-- name: ListDueDeliveries :many
-- tenancy: root (dispatcher)
SELECT * FROM deliveries
WHERE delivered_at IS NULL AND failed_at IS NULL AND next_attempt_at <= ?
ORDER BY next_attempt_at, created_at, id
LIMIT ?;

-- name: CountEarlierPendingDeliveries :one
-- tenancy: root (dispatcher keeps per-monitor order)
SELECT COUNT(*) FROM deliveries
WHERE monitor_id = ? AND channel_id = ? AND delivered_at IS NULL AND failed_at IS NULL AND created_at < ?;

-- name: MarkDeliveryDelivered :exec
-- tenancy: root (dispatcher)
UPDATE deliveries SET delivered_at = ?, attempt = ?, error = NULL WHERE id = ?;

-- name: RescheduleDelivery :exec
-- tenancy: root (dispatcher)
UPDATE deliveries SET attempt = ?, next_attempt_at = ?, error = ? WHERE id = ?;

-- name: MarkDeliveryFailed :exec
-- tenancy: root (dispatcher)
UPDATE deliveries SET attempt = ?, failed_at = ?, error = ? WHERE id = ?;

-- name: LastDeliveryForRoute :one
-- tenancy: root (dispatcher, repeat_every)
SELECT * FROM deliveries
WHERE monitor_id = ? AND route_id = ?
ORDER BY created_at DESC, id DESC
LIMIT 1;

-- name: ListDeliveriesForEvent :many
SELECT * FROM deliveries WHERE project_id = ? AND event_id = ? ORDER BY created_at, id;

-- name: LastSentForChannel :one
SELECT CAST(COALESCE(MAX(delivered_at), 0) AS INTEGER) AS last_sent
FROM deliveries WHERE project_id = ? AND channel_id = ? AND delivered_at IS NOT NULL;

-- name: ListRecentDeliveries :many
SELECT d.*, c.name AS channel_name, c.kind AS channel_kind
FROM deliveries d JOIN channels c ON c.id = d.channel_id
WHERE d.project_id = ?
ORDER BY d.created_at DESC, d.id DESC
LIMIT ?;

-- name: CountPendingDeliveries :one
-- tenancy: root (metrics endpoint)
SELECT COUNT(*) FROM deliveries WHERE delivered_at IS NULL AND failed_at IS NULL;
