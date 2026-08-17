-- +goose NO TRANSACTION
-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '30min';

-- Existing V1 tables stay writable while these V2 access paths are built.
-- The preflight drops only invalid same-name indexes. IF NOT EXISTS preserves
-- valid indexes when a later NO TRANSACTION statement fails and Goose retries;
-- the validation block below still prevents an invalid index from being
-- silently accepted.
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agent_activity_log_agent_seq
    ON agent_activity_log(agent_id, agent_seq)
    WHERE agent_seq IS NOT NULL;
CREATE UNIQUE INDEX CONCURRENTLY IF NOT EXISTS uq_agent_activity_log_source_event
    ON agent_activity_log(source_event_id)
    WHERE source_event_id IS NOT NULL;
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_activity_log_agent_log_id
    ON agent_activity_log(agent_id, log_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agent_activity_log_created_at
    ON agent_activity_log(created_at);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conversations_v2_participant_a
    ON conversations(participant_a, status, updated_at DESC, conv_id DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conversations_v2_participant_b
    ON conversations(participant_b, status, updated_at DESC, conv_id DESC);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_private_messages_v2_receiver_conv_unread
    ON private_messages(receiver_id, conv_id)
    WHERE is_read = FALSE;

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_agents_legacy_normalized_email
    ON agents ((lower(btrim(email))))
    WHERE email_kind = 'legacy_real';

ALTER TABLE agents VALIDATE CONSTRAINT chk_agents_email_kind;

-- +goose StatementBegin
DO $$
DECLARE
    index_name TEXT;
BEGIN
    FOREACH index_name IN ARRAY ARRAY[
        'uq_agent_activity_log_agent_seq',
        'uq_agent_activity_log_source_event',
        'idx_agent_activity_log_agent_log_id',
        'idx_agent_activity_log_created_at',
        'idx_conversations_v2_participant_a',
        'idx_conversations_v2_participant_b',
        'idx_private_messages_v2_receiver_conv_unread',
        'idx_agents_legacy_normalized_email'
    ] LOOP
        IF NOT EXISTS (
            SELECT 1 FROM pg_class c
            JOIN pg_namespace n ON n.oid = c.relnamespace
            JOIN pg_index i ON i.indexrelid = c.oid
            WHERE n.nspname = 'public' AND c.relname = index_name
              AND i.indisvalid AND i.indisready
        ) THEN
            RAISE EXCEPTION 'Console V2 index % is missing or invalid', index_name;
        END IF;
    END LOOP;
END $$;
-- +goose StatementEnd

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM agent_principals LIMIT 1) THEN
        RAISE EXCEPTION 'unsafe Console V2 schema rollback: disable feature flags instead';
    END IF;
END $$;
-- +goose StatementEnd

DROP INDEX CONCURRENTLY IF EXISTS idx_agents_legacy_normalized_email;
DROP INDEX CONCURRENTLY IF EXISTS idx_private_messages_v2_receiver_conv_unread;
DROP INDEX CONCURRENTLY IF EXISTS idx_conversations_v2_participant_b;
DROP INDEX CONCURRENTLY IF EXISTS idx_conversations_v2_participant_a;
DROP INDEX CONCURRENTLY IF EXISTS idx_agent_activity_log_created_at;
DROP INDEX CONCURRENTLY IF EXISTS idx_agent_activity_log_agent_log_id;
DROP INDEX CONCURRENTLY IF EXISTS uq_agent_activity_log_source_event;
DROP INDEX CONCURRENTLY IF EXISTS uq_agent_activity_log_agent_seq;
