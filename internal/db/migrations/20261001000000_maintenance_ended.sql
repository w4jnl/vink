-- migrate:up
ALTER TABLE maintenance ADD COLUMN ended_until INTEGER;

-- migrate:down
ALTER TABLE maintenance DROP COLUMN ended_until;
