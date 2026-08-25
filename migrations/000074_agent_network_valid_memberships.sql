-- +goose Up

-- A network member number represents the order in which an Agent became a
-- usable EigenFlux member. Provisioned shells that never completed a profile
-- must not consume a visible position.
DROP TRIGGER IF EXISTS trg_agents_network_membership ON agents;

TRUNCATE TABLE agent_network_memberships;
ALTER SEQUENCE agent_network_member_no_seq RESTART WITH 1;

INSERT INTO agent_network_memberships (agent_id, member_no, joined_at)
SELECT
    agent_id,
    ROW_NUMBER() OVER (ORDER BY profile_completed_at, created_at, agent_id),
    created_at
FROM agents
WHERE COALESCE(profile_completed_at, 0) > 0
ORDER BY profile_completed_at, created_at, agent_id;

SELECT setval(
    'agent_network_member_no_seq',
    GREATEST(COALESCE((SELECT MAX(member_no) FROM agent_network_memberships), 0) + 1, 1),
    false
);

-- +goose StatementBegin
CREATE OR REPLACE FUNCTION ensure_agent_network_membership()
RETURNS TRIGGER AS $$
BEGIN
    IF COALESCE(NEW.profile_completed_at, 0) <= 0 THEN
        RETURN NEW;
    END IF;

    INSERT INTO agent_network_memberships (agent_id, joined_at)
    VALUES (NEW.agent_id, NEW.created_at)
    ON CONFLICT (agent_id) DO NOTHING;
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;
-- +goose StatementEnd

CREATE TRIGGER trg_agents_network_membership
AFTER INSERT OR UPDATE OF profile_completed_at ON agents
FOR EACH ROW EXECUTE FUNCTION ensure_agent_network_membership();

-- +goose Down

DROP TRIGGER IF EXISTS trg_agents_network_membership ON agents;

TRUNCATE TABLE agent_network_memberships;
ALTER SEQUENCE agent_network_member_no_seq RESTART WITH 1;

INSERT INTO agent_network_memberships (agent_id, member_no, joined_at)
SELECT
    agent_id,
    ROW_NUMBER() OVER (ORDER BY created_at, agent_id),
    created_at
FROM agents
ORDER BY created_at, agent_id;

SELECT setval(
    'agent_network_member_no_seq',
    GREATEST(COALESCE((SELECT MAX(member_no) FROM agent_network_memberships), 0) + 1, 1),
    false
);

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

CREATE TRIGGER trg_agents_network_membership
AFTER INSERT ON agents
FOR EACH ROW EXECUTE FUNCTION ensure_agent_network_membership();
