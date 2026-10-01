-- migrate:up
-- An API key may now belong to an org without a project (project_id NULL):
-- an org key, for org-wide export and apply. SQLite cannot drop NOT NULL,
-- so the table is rebuilt; nothing references api_keys.
CREATE TABLE api_keys_new (
  id TEXT PRIMARY KEY,
  project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  hash TEXT NOT NULL,
  access TEXT NOT NULL CHECK (access IN ('ro', 'rw')),
  created_by TEXT,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER,
  revoked_at INTEGER
);
INSERT INTO api_keys_new SELECT id, project_id, org_id, name, prefix, hash, access, created_by, created_at, last_used_at, revoked_at FROM api_keys;
DROP TABLE api_keys;
ALTER TABLE api_keys_new RENAME TO api_keys;
CREATE INDEX api_keys_prefix ON api_keys(prefix);
CREATE INDEX api_keys_project ON api_keys(project_id);
CREATE INDEX api_keys_org ON api_keys(org_id);

-- migrate:down
DELETE FROM api_keys WHERE project_id IS NULL;
CREATE TABLE api_keys_old (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  hash TEXT NOT NULL,
  access TEXT NOT NULL CHECK (access IN ('ro', 'rw')),
  created_by TEXT,
  created_at INTEGER NOT NULL,
  last_used_at INTEGER,
  revoked_at INTEGER
);
INSERT INTO api_keys_old SELECT id, project_id, org_id, name, prefix, hash, access, created_by, created_at, last_used_at, revoked_at FROM api_keys;
DROP TABLE api_keys;
ALTER TABLE api_keys_old RENAME TO api_keys;
CREATE INDEX api_keys_prefix ON api_keys(prefix);
CREATE INDEX api_keys_project ON api_keys(project_id);
