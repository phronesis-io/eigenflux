-- +goose Up
-- +goose StatementBegin
CREATE TABLE conversations (
    conv_id       BIGINT PRIMARY KEY,
    participant_a BIGINT NOT NULL,
    participant_b BIGINT NOT NULL,
    initiator_id  BIGINT NOT NULL,
    last_sender_id BIGINT NOT NULL,
    origin_type   VARCHAR(20),
    origin_id     BIGINT,
    msg_count     INT NOT NULL DEFAULT 0,
    status        SMALLINT NOT NULL DEFAULT 0,
    updated_at    BIGINT NOT NULL,
    participant_a_name VARCHAR(100) NOT NULL DEFAULT '',
    participant_b_name VARCHAR(100) NOT NULL DEFAULT '',
    UNIQUE(participant_a, participant_b, origin_id)
);
CREATE INDEX idx_conv_user_a_updated ON conversations (participant_a, updated_at DESC);
CREATE INDEX idx_conv_user_b_updated ON conversations (participant_b, updated_at DESC);
CREATE INDEX idx_conv_origin_id ON conversations (origin_id) WHERE origin_id > 0;

CREATE TABLE private_messages (
    msg_id      BIGINT PRIMARY KEY,
    conv_id     BIGINT NOT NULL REFERENCES conversations(conv_id) ON DELETE CASCADE,
    sender_id   BIGINT NOT NULL,
    receiver_id BIGINT NOT NULL,
    content     TEXT NOT NULL,
    is_read     BOOLEAN NOT NULL DEFAULT FALSE,
    created_at  BIGINT NOT NULL,
    sender_name   VARCHAR(100) NOT NULL DEFAULT '',
    receiver_name VARCHAR(100) NOT NULL DEFAULT ''
);
CREATE INDEX idx_pm_conv_msg ON private_messages (conv_id, msg_id DESC);
CREATE INDEX idx_pm_receiver_unread ON private_messages (receiver_id, is_read, msg_id ASC) WHERE is_read = FALSE;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP TABLE IF EXISTS private_messages;
DROP TABLE IF EXISTS conversations;
-- +goose StatementEnd
