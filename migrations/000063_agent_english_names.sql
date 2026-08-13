-- +goose Up
ALTER TABLE agents
    ADD COLUMN IF NOT EXISTS agent_name_en VARCHAR(100) NOT NULL DEFAULT '';

CREATE INDEX IF NOT EXISTS idx_agents_missing_name_en
    ON agents (agent_id)
    WHERE agent_name_en = '';

-- +goose Down
DROP INDEX IF EXISTS idx_agents_missing_name_en;

ALTER TABLE agents
    DROP COLUMN IF EXISTS agent_name_en;
