-- +goose NO TRANSACTION
-- +goose Up

CREATE SEQUENCE IF NOT EXISTS agent_network_member_no_seq;

CREATE TABLE IF NOT EXISTS agent_network_memberships (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    member_no BIGINT NOT NULL UNIQUE DEFAULT nextval('agent_network_member_no_seq'),
    joined_at BIGINT NOT NULL
);

-- Existing agents keep a deterministic, stable order. This first pass is
-- intentionally unlocked so a large historical backfill does not pause
-- normal registration traffic.
INSERT INTO agent_network_memberships (agent_id, joined_at)
SELECT agent_id, created_at
FROM agents
ORDER BY created_at, agent_id
ON CONFLICT (agent_id) DO NOTHING;

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ensure_agent_network_membership()
RETURNS TRIGGER AS $$
BEGIN
    INSERT INTO agent_network_memberships (agent_id, joined_at)
    VALUES (NEW.agent_id, NEW.created_at)
    ON CONFLICT (agent_id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

-- Close the small gap between the unlocked backfill and trigger creation.
-- The lock is held only while the normally tiny delta is inserted.
BEGIN;
LOCK TABLE agents IN SHARE ROW EXCLUSIVE MODE;
DROP TRIGGER IF EXISTS trg_agents_network_membership ON agents;
CREATE TRIGGER trg_agents_network_membership
AFTER INSERT ON agents
FOR EACH ROW EXECUTE FUNCTION ensure_agent_network_membership();
INSERT INTO agent_network_memberships (agent_id, joined_at)
SELECT agent_id, created_at
FROM agents
ORDER BY created_at, agent_id
ON CONFLICT (agent_id) DO NOTHING;
COMMIT;

-- +goose Down

DROP TRIGGER IF EXISTS trg_agents_network_membership ON agents;
DROP FUNCTION IF EXISTS ensure_agent_network_membership();
DROP TABLE IF EXISTS agent_network_memberships;
DROP SEQUENCE IF EXISTS agent_network_member_no_seq;
