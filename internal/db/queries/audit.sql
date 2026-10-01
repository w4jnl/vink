-- name: InsertAudit :exec
-- tenancy: root (written inside the transaction of the action, for its scope)
INSERT INTO audit (id, at, actor, actor_kind, actor_id, org_id, project_id, act, target, target_id, spec_before, spec_after, detail, via, request_id, remote_addr)
VALUES (sqlc.arg(id), sqlc.arg(at), sqlc.arg(actor), sqlc.arg(actor_kind), sqlc.arg(actor_id), sqlc.arg(org_id), sqlc.arg(project_id), sqlc.arg(act), sqlc.arg(target), sqlc.arg(target_id), sqlc.arg(spec_before), sqlc.arg(spec_after), sqlc.arg(detail), sqlc.arg(via), sqlc.arg(request_id), sqlc.arg(remote_addr));

-- name: ListAudit :many
-- tenancy: root (the service scopes by org_id and project_id below)
-- The log: admin actions from audit and state flips from events, newest
-- first, 51 rows so the caller knows whether an older page exists.
SELECT a.id, a.at, 'audit' AS source, a.actor, a.actor_kind, a.actor_id, a.org_id, a.project_id, a.act, a.target, a.target_id, a.spec_before, a.spec_after, a.detail, a.via, a.request_id, a.remote_addr
FROM audit a
WHERE (sqlc.arg(org_id) = '' OR a.org_id = sqlc.arg(org_id))
  AND (sqlc.arg(project_id) = '' OR a.project_id = sqlc.arg(project_id))
  AND (sqlc.arg(org_only) = 0 OR a.project_id IS NULL)
  AND (sqlc.arg(project_only) = 0 OR a.project_id IS NOT NULL)
  AND (sqlc.arg(actor) = '' OR a.actor = sqlc.arg(actor))
  AND a.at >= sqlc.arg(since)
  AND (a.at < sqlc.arg(before_at) OR (a.at = sqlc.arg(before_at) AND a.id < sqlc.arg(before_id)))
  AND ((sqlc.arg(changes) = 1 AND NOT (a.act LIKE 'user.%' OR a.act LIKE 'member.%' OR a.act LIKE 'invite.%' OR a.act LIKE '%key.%'))
    OR (sqlc.arg(access) = 1 AND (a.act LIKE 'user.%' OR a.act LIKE 'member.%' OR a.act LIKE 'invite.%' OR a.act LIKE '%key.%')))
UNION ALL
SELECT e.id, e.at, 'event' AS source, 'vink' AS actor, 'system' AS actor_kind, NULL AS actor_id, p.org_id, e.project_id, 'state.' || e.to_state AS act, m.slug AS target, m.id AS target_id,
  NULL AS spec_before, NULL AS spec_after, json_object('from', e.from_state, 'reason', e.reason) AS detail,
  CASE WHEN o.source IS NULL THEN 'vink' WHEN o.source = 'local' THEN 'checker' WHEN o.source LIKE 'agent:%' THEN o.source ELSE 'ping' END AS via,
  NULL AS request_id, NULL AS remote_addr
FROM events e JOIN monitors m ON m.id = e.monitor_id JOIN projects p ON p.id = e.project_id LEFT JOIN observations o ON o.id = e.observation_id
WHERE sqlc.arg(state) = 1 AND sqlc.arg(org_only) = 0
  AND (sqlc.arg(org_id) = '' OR p.org_id = sqlc.arg(org_id))
  AND (sqlc.arg(project_id) = '' OR e.project_id = sqlc.arg(project_id))
  AND sqlc.arg(actor) = ''
  AND e.at >= sqlc.arg(since)
  AND (e.at < sqlc.arg(before_at) OR (e.at = sqlc.arg(before_at) AND e.id < sqlc.arg(before_id)))
ORDER BY 2 DESC, 1 DESC
LIMIT 51;

-- name: CountAudit :one
-- tenancy: root (the service scopes by org_id and project_id)
SELECT
  CAST(COALESCE(SUM(CASE WHEN act LIKE 'user.%' OR act LIKE 'member.%' OR act LIKE 'invite.%' OR act LIKE '%key.%' THEN 1 ELSE 0 END), 0) AS INTEGER) AS access,
  CAST(COALESCE(SUM(CASE WHEN act LIKE 'user.%' OR act LIKE 'member.%' OR act LIKE 'invite.%' OR act LIKE '%key.%' THEN 0 ELSE 1 END), 0) AS INTEGER) AS changes
FROM audit
WHERE (sqlc.arg(org_id) = '' OR org_id = sqlc.arg(org_id))
  AND (sqlc.arg(project_id) = '' OR project_id = sqlc.arg(project_id))
  AND (sqlc.arg(org_only) = 0 OR project_id IS NULL)
  AND (sqlc.arg(project_only) = 0 OR project_id IS NOT NULL)
  AND at >= sqlc.arg(since);

-- name: CountStateEvents :one
-- tenancy: root (the service scopes by org_id and project_id)
SELECT COUNT(*) FROM events e JOIN projects p ON p.id = e.project_id
WHERE (sqlc.arg(org_id) = '' OR p.org_id = sqlc.arg(org_id))
  AND (sqlc.arg(project_id) = '' OR e.project_id = sqlc.arg(project_id))
  AND e.at >= sqlc.arg(since);

-- name: ListAuditActors :many
-- tenancy: root (the service scopes by org_id and project_id)
SELECT DISTINCT actor FROM audit
WHERE (sqlc.arg(org_id) = '' OR org_id = sqlc.arg(org_id))
  AND (sqlc.arg(project_id) = '' OR project_id = sqlc.arg(project_id))
  AND at >= sqlc.arg(since)
ORDER BY actor;

-- name: SetInstanceMeta :exec
-- tenancy: root (instance facts)
INSERT INTO instance_meta (name, value, updated_at) VALUES (?, ?, ?)
ON CONFLICT (name) DO UPDATE SET value = excluded.value, updated_at = excluded.updated_at;

-- name: GetInstanceMeta :one
-- tenancy: root (instance facts)
SELECT * FROM instance_meta WHERE name = ?;
