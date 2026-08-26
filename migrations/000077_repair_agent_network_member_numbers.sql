-- +goose Up
-- +goose StatementBegin
DO $$
BEGIN
    -- Serialize the complete repair with the membership trigger. The lock is
    -- acquired before inspecting either table so an in-flight nextval/INSERT
    -- cannot commit after setval and leave a hidden gap or future collision.
    LOCK TABLE agent_network_memberships IN ACCESS EXCLUSIVE MODE;

    -- Migration 72's defensive ON CONFLICT paths evaluated the sequence
    -- default before discarding existing rows. Only rebuild when the stored
    -- public numbers differ from the authoritative valid-member join order.
    IF EXISTS (
        WITH ranked AS (
            SELECT agent.agent_id,
                   ROW_NUMBER() OVER (
                       ORDER BY agent.profile_completed_at, agent.created_at, agent.agent_id
                   ) AS member_no
            FROM agents agent
            WHERE COALESCE(agent.profile_completed_at, 0) > 0
        )
        SELECT 1
        FROM ranked
        FULL JOIN agent_network_memberships membership
          ON membership.agent_id = ranked.agent_id
        WHERE ranked.agent_id IS NULL
           OR membership.agent_id IS NULL
           OR membership.member_no <> ranked.member_no
        LIMIT 1
    ) THEN
        DELETE FROM agent_network_memberships;

        INSERT INTO agent_network_memberships (agent_id, member_no, joined_at)
        WITH ranked AS (
            SELECT agent.agent_id,
                   agent.created_at AS joined_at,
                   ROW_NUMBER() OVER (
                       ORDER BY agent.profile_completed_at, agent.created_at, agent.agent_id
                   ) AS member_no
            FROM agents agent
            WHERE COALESCE(agent.profile_completed_at, 0) > 0
        )
        SELECT agent_id, member_no, joined_at
        FROM ranked
        ORDER BY member_no;

    END IF;

    -- Sequence defaults may have advanced even when every persisted public
    -- number is already correct, so repair the allocator under the same lock.
    PERFORM setval(
        'agent_network_member_no_seq',
        GREATEST(COALESCE((SELECT MAX(member_no) FROM agent_network_memberships), 0), 1),
        EXISTS (SELECT 1 FROM agent_network_memberships)
    );
END $$;
-- +goose StatementEnd

-- +goose Down

-- Stable public numbers cannot be safely restored to their former sparse
-- values. Schema rollback remains a no-op; feature rollback uses flags.
SELECT setval(
    'agent_network_member_no_seq',
    GREATEST(COALESCE((SELECT MAX(member_no) FROM agent_network_memberships), 0), 1),
    EXISTS (SELECT 1 FROM agent_network_memberships)
);
