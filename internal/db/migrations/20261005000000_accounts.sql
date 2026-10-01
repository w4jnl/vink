-- migrate:up
-- Accounts: where a user came from, disabled users, two-factor with the
-- secret sealed by the keyring, recovery codes, richer sessions, the
-- step between password and code, invites, reset links and instance facts.
ALTER TABLE users ADD COLUMN source TEXT NOT NULL DEFAULT 'local' CHECK (source IN ('local', 'proxy', 'oidc'));
ALTER TABLE users ADD COLUMN disabled_at INTEGER;
ALTER TABLE users ADD COLUMN disabled_by TEXT;
ALTER TABLE users ADD COLUMN totp_secret TEXT;
ALTER TABLE users ADD COLUMN totp_enabled_at INTEGER;
ALTER TABLE users ADD COLUMN totp_last_step INTEGER NOT NULL DEFAULT 0;
ALTER TABLE users ADD COLUMN password_changed_at INTEGER;
UPDATE users SET source = CASE WHEN password_hash IS NULL THEN 'proxy' ELSE 'local' END;

CREATE TABLE recovery_codes (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  prefix TEXT NOT NULL,
  hash TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  used_at INTEGER,
  PRIMARY KEY (user_id, prefix)
);

ALTER TABLE sessions ADD COLUMN user_agent TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN ip TEXT NOT NULL DEFAULT '';
ALTER TABLE sessions ADD COLUMN last_seen_at INTEGER;

CREATE TABLE login_challenges (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  attempts INTEGER NOT NULL DEFAULT 0,
  ip TEXT NOT NULL DEFAULT '',
  user_agent TEXT NOT NULL DEFAULT '',
  next_path TEXT NOT NULL DEFAULT ''
);

CREATE TABLE invites (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
  note TEXT NOT NULL DEFAULT '',
  token_hash TEXT NOT NULL UNIQUE,
  created_by TEXT REFERENCES users(id) ON DELETE SET NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER,
  used_by TEXT,
  revoked_at INTEGER
);
CREATE INDEX invites_org ON invites(org_id, created_at DESC);

CREATE TABLE reset_tokens (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  token_hash TEXT NOT NULL UNIQUE,
  created_by TEXT,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  used_at INTEGER
);

CREATE TABLE instance_meta (
  name TEXT PRIMARY KEY,
  value TEXT NOT NULL,
  updated_at INTEGER NOT NULL
);

-- migrate:down
DROP TABLE instance_meta;
DROP TABLE reset_tokens;
DROP INDEX invites_org;
DROP TABLE invites;
DROP TABLE login_challenges;
ALTER TABLE sessions DROP COLUMN last_seen_at;
ALTER TABLE sessions DROP COLUMN ip;
ALTER TABLE sessions DROP COLUMN user_agent;
DROP TABLE recovery_codes;
ALTER TABLE users DROP COLUMN password_changed_at;
ALTER TABLE users DROP COLUMN totp_last_step;
ALTER TABLE users DROP COLUMN totp_enabled_at;
ALTER TABLE users DROP COLUMN totp_secret;
ALTER TABLE users DROP COLUMN disabled_by;
ALTER TABLE users DROP COLUMN disabled_at;
ALTER TABLE users DROP COLUMN source;
