-- +goose Up
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS runtime_name VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS runtime_version VARCHAR(64) NOT NULL DEFAULT '';

UPDATE agent_settings
SET runtime_name = LEFT(LOWER(SPLIT_PART(client_host, '/', 1)), 64),
    runtime_version = CASE
        WHEN POSITION('/' IN client_host) > 0
        THEN LEFT(SUBSTRING(client_host FROM POSITION('/' IN client_host) + 1), 64)
        ELSE ''
    END
WHERE client_host <> ''
  AND client_host <> 'terminal'
  AND runtime_name = '';

-- +goose Down
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS runtime_version,
    DROP COLUMN IF EXISTS runtime_name;
