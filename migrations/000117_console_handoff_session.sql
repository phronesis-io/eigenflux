-- +goose Up
ALTER TABLE console_v2_handoffs
    ADD COLUMN consumed_session_id VARCHAR(128) NULL
    REFERENCES console_v2_sessions(session_id) ON DELETE SET NULL;

-- +goose Down
ALTER TABLE console_v2_handoffs DROP COLUMN consumed_session_id;
