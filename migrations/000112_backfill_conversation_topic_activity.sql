-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- +goose StatementBegin
DO $$
DECLARE
    candidate RECORD;
    latest_event RECORD;
    changed INTEGER;
    repaired INTEGER := 0;
BEGIN
    -- Lock before reading event/message evidence. A concurrent PM or topic
    -- update must finish first or run after this repair, never be overwritten.
    FOR candidate IN
        SELECT conv_id, participant_a, participant_b, topic_status, updated_at
        FROM conversations
        WHERE status = 0 AND msg_count >= 1
          AND updated_at BETWEEN 946684800 AND 9999999999
        ORDER BY conv_id
        FOR UPDATE
    LOOP
        SELECT actor_id, previous_status, new_status, created_at
        INTO latest_event
        FROM conversation_topic_events
        WHERE conv_id = candidate.conv_id
        ORDER BY event_id DESC
        LIMIT 1;

        IF NOT FOUND THEN
            CONTINUE;
        END IF;

        -- The latest effective, participant-owned topic event must explain
        -- both the stored state and its truncated seconds timestamp.
        IF latest_event.actor_id NOT IN (candidate.participant_a, candidate.participant_b)
           OR latest_event.previous_status = latest_event.new_status
           OR latest_event.new_status <> candidate.topic_status
           OR latest_event.created_at < 1000000000000
           OR latest_event.created_at > (EXTRACT(EPOCH FROM clock_timestamp()) * 1000)::BIGINT
           OR latest_event.created_at / 1000 <> candidate.updated_at
           OR EXISTS (
               SELECT 1 FROM private_messages
               WHERE conv_id = candidate.conv_id
                 AND created_at > latest_event.created_at
           ) THEN
            CONTINUE;
        END IF;

        UPDATE conversations
        SET updated_at = latest_event.created_at
        WHERE conv_id = candidate.conv_id
          AND status = 0 AND msg_count >= 1
          AND topic_status = candidate.topic_status
          AND updated_at = candidate.updated_at;
        GET DIAGNOSTICS changed = ROW_COUNT;
        repaired := repaired + changed;
    END LOOP;

    RAISE NOTICE 'Conversation topic activity backfill: repaired % row(s)', repaired;
END $$;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DO $$
BEGIN
    RAISE EXCEPTION 'Conversation activity backfill cannot be reversed: preserve repaired and subsequent activity timestamps';
END $$;
-- +goose StatementEnd
