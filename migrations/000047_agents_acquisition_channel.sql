-- +goose Up
-- +goose StatementBegin
-- User-level acquisition source (被哪个渠道带来注册), written first-wins from
-- the install report when it carries a verified identity (same guards as
-- invited_by_code: agent_id+email pair must match and the account must have
-- registered after the install entry was minted). Values are install_tokens
-- channel buckets: xiaohongshu / twitter / <channel-code name> / user / etc.
-- Empty for accounts registered before this landed — that mapping was never
-- persisted and cannot be backfilled.
ALTER TABLE agents ADD COLUMN acquisition_channel TEXT NOT NULL DEFAULT '';
CREATE INDEX idx_agents_acquisition_channel ON agents(acquisition_channel) WHERE acquisition_channel <> '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
DROP INDEX IF EXISTS idx_agents_acquisition_channel;
ALTER TABLE agents DROP COLUMN acquisition_channel;
-- +goose StatementEnd
