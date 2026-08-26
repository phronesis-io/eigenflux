-- +goose NO TRANSACTION
-- +goose Up

-- Destructive by product decision: agent_attention.v1 does not import or
-- expose legacy Attention. The procedure commits every 1,000 rows so the
-- purge does not retain a table-sized MVCC snapshot or a table-sized lock set.
SET lock_timeout = '2s';
SET statement_timeout = '30min';

-- +goose StatementBegin
CREATE OR REPLACE PROCEDURE purge_legacy_attention_v1(batch_size integer)
LANGUAGE plpgsql
AS $$
DECLARE
    attention_ids bigint[];
    command_ids bigint[];
BEGIN
    LOOP
        SELECT array_agg(target.attention_id) INTO attention_ids
        FROM (
            SELECT item.attention_id
            FROM agent_attention_items item
            WHERE item.producer = 'legacy'
            ORDER BY item.attention_id
            LIMIT batch_size
            FOR UPDATE SKIP LOCKED
        ) target;
        EXIT WHEN attention_ids IS NULL;

        DELETE FROM control_wakeup_outbox outbox
        USING agent_commands command
        WHERE outbox.event_type = 'command_available'
          AND outbox.entity_id = command.command_id
          AND command.attention_id = ANY(attention_ids);
        DELETE FROM agent_commands WHERE attention_id = ANY(attention_ids);
        DELETE FROM agent_attention_items WHERE attention_id = ANY(attention_ids);
        COMMIT;
        PERFORM pg_sleep(0.01);
    END LOOP;

    LOOP
        SELECT array_agg(target.command_id) INTO command_ids
        FROM (
            SELECT command.command_id
            FROM agent_commands command
            WHERE command.command_type = 'attention_action'
            ORDER BY command.command_id
            LIMIT batch_size
            FOR UPDATE SKIP LOCKED
        ) target;
        EXIT WHEN command_ids IS NULL;

        DELETE FROM control_wakeup_outbox outbox
        WHERE outbox.event_type = 'command_available'
          AND outbox.entity_id = ANY(command_ids);
        DELETE FROM agent_commands WHERE command_id = ANY(command_ids);
        COMMIT;
        PERFORM pg_sleep(0.01);
    END LOOP;
END
$$;
-- +goose StatementEnd

CALL purge_legacy_attention_v1(1000);
DROP PROCEDURE purge_legacy_attention_v1(integer);
DROP INDEX CONCURRENTLY IF EXISTS uq_agent_commands_attention_action_key;

UPDATE agent_credential_sessions session
SET scopes = array_append(session.scopes, 'attention:write')
WHERE session.revoked_at IS NULL
  AND NOT ('attention:write' = ANY(session.scopes))
  AND EXISTS (
      SELECT 1 FROM agent_principals principal
      JOIN agent_onboarding_v2 onboarding ON onboarding.agent_id = principal.agent_id
      WHERE principal.principal_id = session.principal_id
        AND principal.revoked_at IS NULL
        AND onboarding.state = 'completed'
  );

ANALYZE agent_attention_items;
ANALYZE agent_commands;

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'legacy Attention purge is irreversible';
END $$;
-- +goose StatementEnd
