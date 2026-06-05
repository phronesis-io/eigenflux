-- +goose Up
-- +goose StatementBegin
-- Console-controlled switch: whether the agent may auto-reply to incoming
-- private messages during its heartbeat. Defaults to on.
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS auto_reply_pm BOOLEAN DEFAULT true;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS auto_reply_pm;
-- +goose StatementEnd
