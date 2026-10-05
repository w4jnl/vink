-- migrate:up
-- A status page belongs to a project or, with project_id NULL, to an org,
-- and shows its open incidents, the ones resolved lately, or none. SQLite
-- cannot drop a NOT NULL, so the table is rebuilt; nothing references it.
ALTER TABLE status_pages RENAME TO status_pages_old;
DROP INDEX status_pages_project;
CREATE TABLE status_pages (
  id TEXT PRIMARY KEY,
  org_id TEXT NOT NULL REFERENCES orgs(id) ON DELETE CASCADE,
  project_id TEXT REFERENCES projects(id) ON DELETE CASCADE,
  slug TEXT NOT NULL UNIQUE,
  title TEXT NOT NULL,
  match_tags TEXT NOT NULL DEFAULT '[]',
  -- org pages: the project ids shown, JSON; empty shows every project
  projects TEXT NOT NULL DEFAULT '[]',
  group_by TEXT NOT NULL DEFAULT 'tag' CHECK (group_by IN ('tag', 'project')),
  -- open: open incidents; 7d, 30d, 90d: and those resolved in that window
  incidents TEXT NOT NULL DEFAULT 'open' CHECK (incidents IN ('none', 'open', '7d', '30d', '90d')),
  public BOOLEAN NOT NULL DEFAULT 1,
  password_hash TEXT,
  custom_domain TEXT,
  created_at INTEGER NOT NULL,
  updated_at INTEGER NOT NULL,
  -- a project page groups by tag and lists no projects
  CHECK (project_id IS NULL OR (group_by = 'tag' AND projects = '[]'))
);
INSERT INTO status_pages (id, org_id, project_id, slug, title, match_tags, public, password_hash, custom_domain, created_at, updated_at)
SELECT sp.id, p.org_id, sp.project_id, sp.slug, sp.title, sp.match_tags, sp.public, sp.password_hash, sp.custom_domain, sp.created_at, sp.updated_at
FROM status_pages_old sp JOIN projects p ON p.id = sp.project_id;
DROP TABLE status_pages_old;
CREATE INDEX status_pages_project ON status_pages(project_id);
CREATE INDEX status_pages_org ON status_pages(org_id);

-- migrate:down
ALTER TABLE status_pages RENAME TO status_pages_new;
DROP INDEX status_pages_project;
DROP INDEX status_pages_org;
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
-- org pages have no project to go back to
INSERT INTO status_pages (id, project_id, slug, title, match_tags, public, password_hash, custom_domain, created_at, updated_at)
SELECT id, project_id, slug, title, match_tags, public, password_hash, custom_domain, created_at, updated_at
FROM status_pages_new WHERE project_id IS NOT NULL;
DROP TABLE status_pages_new;
CREATE INDEX status_pages_project ON status_pages(project_id);
