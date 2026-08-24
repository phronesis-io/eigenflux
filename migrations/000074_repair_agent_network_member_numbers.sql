-- +goose Up

-- Migration 72's defensive ON CONFLICT passes evaluated the sequence default
-- before discarding existing rows. That left large gaps which made member_no
-- look like a user count. Rebuild the still-internal numbering once from the
-- authoritative join order, then continue allocating monotonically.
LOCK TABLE agent_network_memberships IN ACCESS EXCLUSIVE MODE;

WITH ranked AS (
    SELECT agent_id, ROW_NUMBER() OVER (ORDER BY joined_at, agent_id) AS member_no
    FROM agent_network_memberships
)
UPDATE agent_network_memberships AS membership
SET member_no = -ranked.member_no
FROM ranked
WHERE membership.agent_id = ranked.agent_id;

UPDATE agent_network_memberships
SET member_no = -member_no
WHERE member_no < 0;

SELECT setval(
    'agent_network_member_no_seq',
    GREATEST(COALESCE((SELECT MAX(member_no) FROM agent_network_memberships), 0), 1),
    EXISTS (SELECT 1 FROM agent_network_memberships)
);

-- +goose Down

-- Stable public numbers cannot be safely restored to their former sparse
-- values. Schema rollback remains a no-op; feature rollback uses flags.
SELECT setval(
    'agent_network_member_no_seq',
    GREATEST(COALESCE((SELECT MAX(member_no) FROM agent_network_memberships), 0), 1),
    EXISTS (SELECT 1 FROM agent_network_memberships)
);
