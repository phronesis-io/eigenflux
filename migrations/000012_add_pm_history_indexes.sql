-- +goose Up
-- +goose StatementBegin

-- For FetchRecentReadMessages: messages agent has already read.
CREATE INDEX idx_pm_receiver_read ON private_messages (receiver_id, msg_id DESC) WHERE is_read = TRUE;

-- For FetchRecentReadMessages: messages agent has sent (any read state).
CREATE INDEX idx_pm_sender_msg ON private_messages (sender_id, msg_id DESC);

-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin

DROP INDEX IF EXISTS idx_pm_sender_msg;
DROP INDEX IF EXISTS idx_pm_receiver_read;

-- +goose StatementEnd
