-- +goose NO TRANSACTION
-- +goose Up
SET lock_timeout = '5s';
SET statement_timeout = '30min';

ALTER TABLE conversations
    ADD COLUMN IF NOT EXISTS topic_status SMALLINT NOT NULL DEFAULT 1;

-- +goose StatementBegin
DO $$
BEGIN
    IF NOT EXISTS (
        SELECT 1 FROM pg_constraint
        WHERE conrelid = 'conversations'::regclass
          AND conname = 'chk_conversations_topic_status'
    ) THEN
        ALTER TABLE conversations
            ADD CONSTRAINT chk_conversations_topic_status
            CHECK (topic_status BETWEEN 0 AND 2) NOT VALID;
    END IF;
END $$;
-- +goose StatementEnd

ALTER TABLE conversations VALIDATE CONSTRAINT chk_conversations_topic_status;

CREATE TABLE IF NOT EXISTS conversation_topic_events (
    event_id        BIGSERIAL PRIMARY KEY,
    conv_id         BIGINT NOT NULL REFERENCES conversations(conv_id) ON DELETE CASCADE,
    actor_id        BIGINT NOT NULL,
    previous_status SMALLINT NOT NULL,
    new_status      SMALLINT NOT NULL,
    created_at      BIGINT NOT NULL,
    CONSTRAINT chk_conversation_topic_events_previous CHECK (previous_status BETWEEN 0 AND 2),
    CONSTRAINT chk_conversation_topic_events_new CHECK (new_status BETWEEN 0 AND 2)
);

CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conversations_topic_participant_a
    ON conversations(participant_a, status, topic_status, updated_at, conv_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conversations_topic_participant_b
    ON conversations(participant_b, status, topic_status, updated_at, conv_id);
CREATE INDEX CONCURRENTLY IF NOT EXISTS idx_conversation_topic_events_conv
    ON conversation_topic_events(conv_id, event_id);

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS idx_conversation_topic_events_conv;
DROP INDEX CONCURRENTLY IF EXISTS idx_conversations_topic_participant_b;
DROP INDEX CONCURRENTLY IF EXISTS idx_conversations_topic_participant_a;
DROP TABLE IF EXISTS conversation_topic_events;
ALTER TABLE conversations DROP CONSTRAINT IF EXISTS chk_conversations_topic_status;
ALTER TABLE conversations DROP COLUMN IF EXISTS topic_status;
