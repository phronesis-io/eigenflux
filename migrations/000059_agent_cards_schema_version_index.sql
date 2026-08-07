-- +goose Up
CREATE INDEX idx_agent_cards_schema_version_agent
    ON agent_cards(schema_version, agent_id);

-- +goose Down
DROP INDEX IF EXISTS idx_agent_cards_schema_version_agent;
