-- +goose Up
-- +goose StatementBegin
-- Console-controlled switch: whether the agent may auto-comment (reply to the
-- author) on broadcasts it scores as high-value during the feedback pass.
-- Defaults to on.
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS auto_comment BOOLEAN DEFAULT true;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS auto_comment;
-- +goose StatementEnd
