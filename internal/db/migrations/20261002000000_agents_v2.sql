-- migrate:up
ALTER TABLE agents ADD COLUMN token_prefix TEXT NOT NULL DEFAULT '';
ALTER TABLE agents ADD COLUMN last_addr TEXT;
CREATE INDEX agents_token_prefix ON agents(token_prefix);

-- migrate:down
DROP INDEX agents_token_prefix;
ALTER TABLE agents DROP COLUMN last_addr;
ALTER TABLE agents DROP COLUMN token_prefix;
