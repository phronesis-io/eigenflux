-- +goose Up
-- +goose StatementBegin
-- Agent-reported fields: the agent pushes these up (PUT /agents/me/settings),
-- distinct from the console-owned recurring_publish / feed_poll_interval.
ALTER TABLE agent_settings
    ADD COLUMN IF NOT EXISTS feed_delivery_preference TEXT DEFAULT '',
    ADD COLUMN IF NOT EXISTS mode VARCHAR(20) DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE agent_settings
    DROP COLUMN IF EXISTS feed_delivery_preference,
    DROP COLUMN IF EXISTS mode;
-- +goose StatementEnd
