-- migrate:up
-- A channel may now belong to an org without a project (project_id NULL):
-- an org channel, which only org routes send to. An org route covers
-- some of the org's projects (projects, a JSON list of ids) or, when the
-- list is empty, all of them, new ones included. SQLite cannot drop
-- NOT NULL, so channels is rebuilt; deliveries and route_channels keep
-- naming channels, which the rename points at the new table.
CREATE TABLE channels_new (
  id TEXT PRIMARY KEY,
  project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  name TEXT NOT NULL,
  kind TEXT NOT NULL CHECK (kind IN ('smtp', 'webhook', 'ntfy', 'gotify', 'matrix', 'slackhook', 'alertmanager')),
  config TEXT NOT NULL,
  enabled BOOLEAN NOT NULL DEFAULT 1,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  UNIQUE (project_id, name)
);
INSERT INTO channels_new (id, project_id, org_id, name, kind, config, enabled, created_at, updated_at)
SELECT id, project_id, org_id, name, kind, config, enabled, created_at, updated_at FROM channels;
DROP TABLE channels;
ALTER TABLE channels_new RENAME TO channels;
-- org channel names are unique within the org; NULLs never collide in UNIQUE
CREATE UNIQUE INDEX channels_org_name ON channels(org_id, name) WHERE project_id IS NULL;

CREATE TABLE org_routes (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  -- project ids the route covers; [] covers every project of the org
  projects TEXT NOT NULL DEFAULT '[]',
  match_tags TEXT NOT NULL DEFAULT '[]',
  on_states TEXT NOT NULL DEFAULT '["down","up"]',
  repeat_every_s INTEGER NOT NULL DEFAULT 0,
  priority INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
CREATE INDEX org_routes_org ON org_routes(org_id, priority DESC);
CREATE TABLE org_route_channels (
  route_id TEXT NOT NULL REFERENCES org_routes(id) ON DELETE CASCADE,
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  org_id TEXT NOT NULL,
  PRIMARY KEY (route_id, channel_id)
);
CREATE INDEX org_route_channels_channel ON org_route_channels(channel_id);
CREATE INDEX org_route_channels_org ON org_route_channels(org_id);

-- migrate:down
DELETE FROM deliveries WHERE channel_id IN (SELECT id FROM channels WHERE project_id IS NULL);
DROP TABLE org_route_channels;
DROP TABLE org_routes;
DELETE FROM channels WHERE project_id IS NULL;
CREATE TABLE channels_old (
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
INSERT INTO channels_old (id, project_id, org_id, name, kind, config, enabled, created_at, updated_at)
SELECT id, project_id, org_id, name, kind, config, enabled, created_at, updated_at FROM channels;
DROP TABLE channels;
ALTER TABLE channels_old RENAME TO channels;
