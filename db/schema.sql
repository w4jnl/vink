CREATE TABLE "schema_migrations" (version varchar(128) primary key);
CREATE TABLE users (
  id TEXT PRIMARY KEY,
  subject TEXT NOT NULL UNIQUE,
  email TEXT NOT NULL DEFAULT '',
  display_name TEXT NOT NULL DEFAULT '',
  password_hash TEXT,
  is_instance_admin BOOLEAN NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL
);
CREATE TABLE orgs (
  id TEXT PRIMARY KEY,
  slug TEXT NOT NULL UNIQUE,
  name TEXT NOT NULL,
  quota_monitors INTEGER,
  quota_agents INTEGER,
  created_at INTEGER NOT NULL
);
CREATE TABLE memberships (
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  role TEXT NOT NULL CHECK (role IN ('owner', 'admin', 'member', 'viewer')),
  source TEXT NOT NULL DEFAULT 'local' CHECK (source IN ('local', 'header', 'oidc')),
  created_at INTEGER NOT NULL,
  PRIMARY KEY (user_id, org_id)
);
CREATE INDEX memberships_org ON memberships(org_id);
CREATE TABLE projects (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  timezone TEXT NOT NULL DEFAULT 'UTC',
  ping_key TEXT NOT NULL UNIQUE,
  ping_key_prev TEXT,
  ping_key_prev_until INTEGER,
  created_at INTEGER NOT NULL,
  UNIQUE (org_id, slug)
);
CREATE INDEX projects_ping_key_prev ON projects(ping_key_prev);
CREATE TABLE api_keys (
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
CREATE INDEX api_keys_prefix ON api_keys(prefix);
CREATE INDEX api_keys_project ON api_keys(project_id);
CREATE TABLE monitors (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  slug TEXT NOT NULL,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('heartbeat', 'http', 'tcp', 'dns', 'tls', 'icmp')),
  spec TEXT NOT NULL,
  tags TEXT NOT NULL DEFAULT '[]',
  state TEXT NOT NULL DEFAULT 'new' CHECK (state IN ('new', 'up', 'late', 'down', 'paused')),
  state_since INTEGER NOT NULL,
  last_obs_at INTEGER,
  last_ok_at INTEGER,
  next_due_at INTEGER,
  paused BOOLEAN NOT NULL DEFAULT 0,
  fail_streak INTEGER NOT NULL DEFAULT 0,
  ok_streak INTEGER NOT NULL DEFAULT 0,
  run_started_at INTEGER,
  run_id TEXT,
  agent_id TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (project_id, slug)
);
CREATE INDEX monitors_next_due ON monitors(next_due_at);
CREATE INDEX monitors_org ON monitors(org_id);
CREATE TABLE observations (
  id TEXT PRIMARY KEY,
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  at INTEGER NOT NULL,
  source TEXT NOT NULL,
  signal TEXT NOT NULL CHECK (signal IN ('start', 'ok', 'fail', 'log', 'exit')),
  ok BOOLEAN NOT NULL,
  latency_ms INTEGER,
  exit_code INTEGER,
  run_id TEXT,
  duration_ms INTEGER,
  remote_addr TEXT,
  user_agent TEXT,
  body_ref TEXT,
  detail TEXT NOT NULL DEFAULT '{}'
);
CREATE INDEX observations_monitor_at ON observations(monitor_id, at DESC, id DESC);
CREATE INDEX observations_project_at ON observations(project_id, at);
CREATE INDEX observations_at ON observations(at);
CREATE TABLE bodies (
  observation_id TEXT PRIMARY KEY REFERENCES observations(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  content BLOB NOT NULL,
  content_type TEXT NOT NULL DEFAULT '',
  created_at INTEGER NOT NULL
);
CREATE INDEX bodies_created ON bodies(created_at);
CREATE TABLE events (
  id TEXT PRIMARY KEY,
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  at INTEGER NOT NULL,
  from_state TEXT NOT NULL,
  to_state TEXT NOT NULL,
  reason TEXT NOT NULL DEFAULT '',
  observation_id TEXT
);
CREATE INDEX events_monitor_at ON events(monitor_id, at DESC, id DESC);
CREATE INDEX events_project_at ON events(project_id, at DESC);
CREATE TABLE incidents (
  id TEXT PRIMARY KEY,
  monitor_id TEXT NOT NULL REFERENCES monitors(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  opened_at INTEGER NOT NULL,
  resolved_at INTEGER,
  acked_by TEXT,
  acked_at INTEGER,
  open_event_id TEXT NOT NULL,
  close_event_id TEXT
);
CREATE INDEX incidents_project_open ON incidents(project_id, resolved_at, opened_at DESC);
CREATE INDEX incidents_monitor ON incidents(monitor_id);
CREATE TABLE channels (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('smtp', 'webhook', 'ntfy', 'gotify', 'matrix', 'slackhook', 'alertmanager')),
  config TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (project_id, name)
);
CREATE TABLE routes (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  match_tags TEXT NOT NULL DEFAULT '[]',
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  on_states TEXT NOT NULL DEFAULT '["down","up"]',
  repeat_every_s INTEGER NOT NULL DEFAULT 0,
  priority INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX routes_project ON routes(project_id, priority DESC);
CREATE TABLE deliveries (
  id TEXT PRIMARY KEY,
  event_id TEXT NOT NULL REFERENCES events(id) ON DELETE CASCADE,
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  monitor_id TEXT NOT NULL,
  route_id TEXT,
  kind TEXT NOT NULL,
  repeat BOOLEAN NOT NULL DEFAULT 0,
  attempt INTEGER NOT NULL DEFAULT 0,
  next_attempt_at INTEGER NOT NULL,
  delivered_at INTEGER,
  failed_at INTEGER,
  error TEXT,
  created_at INTEGER NOT NULL
);
CREATE INDEX deliveries_due ON deliveries(next_attempt_at) WHERE delivered_at IS NULL AND failed_at IS NULL;
CREATE INDEX deliveries_monitor ON deliveries(monitor_id, created_at);
CREATE INDEX deliveries_event ON deliveries(event_id);
CREATE TABLE maintenance (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  match_tags TEXT NOT NULL DEFAULT '[]',
  starts_at INTEGER,
  ends_at INTEGER,
  rrule TEXT,
  from_time TEXT,
  to_time TEXT,
  timezone TEXT NOT NULL DEFAULT 'UTC',
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX maintenance_project ON maintenance(project_id);
CREATE TABLE status_pages (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  slug TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  match_tags TEXT NOT NULL DEFAULT '[]',
  public BOOLEAN NOT NULL DEFAULT 1,
  password_hash TEXT,
  custom_domain TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX status_pages_project ON status_pages(project_id);
CREATE TABLE agents (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  token_hash TEXT NOT NULL,
  last_seen_at INTEGER,
  version TEXT,
  labels TEXT NOT NULL DEFAULT '{}',
  created_at INTEGER NOT NULL,
  UNIQUE (org_id, name)
);
CREATE TABLE sessions (
  id TEXT PRIMARY KEY,
  user_id TEXT NOT NULL REFERENCES users(id) ON DELETE CASCADE,
  csrf TEXT NOT NULL,
  created_at INTEGER NOT NULL,
  expires_at INTEGER NOT NULL,
  last_project_id TEXT
);
CREATE INDEX sessions_user ON sessions(user_id);
CREATE INDEX sessions_expires ON sessions(expires_at);
-- Dbmate schema migrations
INSERT INTO "schema_migrations" (version) VALUES
  ('20260927000000');
