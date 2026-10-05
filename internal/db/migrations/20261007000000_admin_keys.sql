-- migrate:up
-- Instance admin API keys (vka_…): they act as an instance admin on
-- /api/v1/admin and nothing else, always expire, and are created only in
-- the web UI or with vink admin key create on the server host.
CREATE TABLE admin_keys (
  id TEXT PRIMARY KEY,
  name TEXT NOT NULL,
  prefix TEXT NOT NULL,
  hash TEXT NOT NULL,
  access TEXT NOT NULL CHECK (access IN ('ro', 'rw')),
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_used_at INTEGER,
  last_used_ip TEXT,
  revoked_at INTEGER
);
CREATE INDEX admin_keys_prefix ON admin_keys(prefix);

-- migrate:down
DROP TABLE admin_keys;
