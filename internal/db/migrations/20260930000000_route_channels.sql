-- migrate:up

-- A route sends to several channels (the apply YAML and the settings
-- screens say `channels: [...]`), so the single routes.channel_id column
-- becomes a join table. SQLite cannot drop a column that carries a
-- foreign key, hence the table rebuild.

CREATE TABLE route_channels (
  route_id TEXT NOT NULL REFERENCES routes(id) ON DELETE CASCADE,
  channel_id TEXT NOT NULL REFERENCES channels(id) ON DELETE CASCADE,
  project_id TEXT NOT NULL,
  PRIMARY KEY (route_id, channel_id)
);
CREATE INDEX route_channels_channel ON route_channels(channel_id);
CREATE INDEX route_channels_project ON route_channels(project_id);

INSERT INTO route_channels (route_id, channel_id, project_id)
SELECT id, channel_id, project_id FROM routes;

CREATE TABLE routes_new (
  id TEXT PRIMARY KEY,
  project_id TEXT NOT NULL REFERENCES projects(id) ON DELETE CASCADE,
  match_tags TEXT NOT NULL DEFAULT '[]',
  on_states TEXT NOT NULL DEFAULT '["down","up"]',
  repeat_every_s INTEGER NOT NULL DEFAULT 0,
  priority INTEGER NOT NULL DEFAULT 0,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL
);
INSERT INTO routes_new (id, project_id, match_tags, on_states, repeat_every_s, priority, created_at, updated_at)
SELECT id, project_id, match_tags, on_states, repeat_every_s, priority, created_at, updated_at FROM routes;
DROP TABLE routes;
ALTER TABLE routes_new RENAME TO routes;
CREATE INDEX routes_project ON routes(project_id, priority DESC);

-- migrate:down

CREATE TABLE routes_old (
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
INSERT INTO routes_old (id, project_id, match_tags, channel_id, on_states, repeat_every_s, priority, created_at, updated_at)
SELECT r.id, r.project_id, r.match_tags, (SELECT channel_id FROM route_channels rc WHERE rc.route_id = r.id ORDER BY channel_id LIMIT 1),
       r.on_states, r.repeat_every_s, r.priority, r.created_at, r.updated_at
FROM routes r WHERE EXISTS (SELECT 1 FROM route_channels rc WHERE rc.route_id = r.id);
DROP TABLE routes;
ALTER TABLE routes_old RENAME TO routes;
CREATE INDEX routes_project ON routes(project_id, priority DESC);
DROP TABLE route_channels;
