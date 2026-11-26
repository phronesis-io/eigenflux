-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '30s';

-- Existing messages and older clients retain NULL: conversation membership
-- and message order are not evidence of the sender's intent to reply.
ALTER TABLE private_messages
    ADD COLUMN message_kind VARCHAR(16),
    ADD COLUMN reply_to_message_id BIGINT,
    ADD CONSTRAINT private_messages_intent_check CHECK (CASE
        WHEN message_kind IS NULL THEN reply_to_message_id IS NULL
        WHEN message_kind = 'contact' THEN reply_to_message_id IS NULL
        WHEN message_kind = 'reply' THEN reply_to_message_id IS NOT NULL AND reply_to_message_id > 0
        ELSE FALSE
    END);

COMMENT ON COLUMN private_messages.message_kind IS
    'Explicit sender intent: contact or reply; NULL is unknown, never backfilled from message order';
COMMENT ON COLUMN private_messages.reply_to_message_id IS
    'For explicit replies, a server-validated inbound message in the same conversation';

-- +goose Down
ALTER TABLE private_messages DROP CONSTRAINT private_messages_intent_check;
ALTER TABLE private_messages DROP COLUMN reply_to_message_id, DROP COLUMN message_kind;
