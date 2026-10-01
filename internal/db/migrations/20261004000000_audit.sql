-- migrate:up
-- One row per admin action, written in the same transaction as the
-- change. Rows are never updated, deleted or pruned.
CREATE TABLE audit (
  id TEXT PRIMARY KEY,
  at INTEGER NOT NULL,
  actor TEXT NOT NULL,
  actor_kind TEXT NOT NULL CHECK (actor_kind IN ('user', 'key', 'system')),
  actor_id TEXT,
  org_id TEXT,
  project_id TEXT,
  act TEXT NOT NULL,
  target TEXT NOT NULL DEFAULT '',
  target_id TEXT,
  spec_before TEXT,
  spec_after TEXT,
  detail TEXT NOT NULL DEFAULT '{}',
  via TEXT NOT NULL DEFAULT '',
  request_id TEXT,
  remote_addr TEXT
);
CREATE INDEX audit_org_at ON audit(org_id, at DESC, id DESC);
CREATE INDEX audit_project_at ON audit(project_id, at DESC, id DESC);
CREATE INDEX audit_at ON audit(at DESC, id DESC);

-- migrate:down
DROP TABLE audit;
