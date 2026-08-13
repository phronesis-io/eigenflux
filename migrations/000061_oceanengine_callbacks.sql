-- +goose Up
ALTER TABLE install_tokens
    ADD COLUMN IF NOT EXISTS oceanengine_click_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS oceanengine_ad_id VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS oceanengine_creative_id VARCHAR(128) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS oceanengine_creative_type VARCHAR(64) NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS oceanengine_cb_active_code INT NOT NULL DEFAULT -1,
    ADD COLUMN IF NOT EXISTS oceanengine_cb_active_sent_at BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS oceanengine_cb_register_code INT NOT NULL DEFAULT -1,
    ADD COLUMN IF NOT EXISTS oceanengine_cb_register_sent_at BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE install_tokens
    DROP COLUMN IF EXISTS oceanengine_cb_register_sent_at,
    DROP COLUMN IF EXISTS oceanengine_cb_register_code,
    DROP COLUMN IF EXISTS oceanengine_cb_active_sent_at,
    DROP COLUMN IF EXISTS oceanengine_cb_active_code,
    DROP COLUMN IF EXISTS oceanengine_creative_type,
    DROP COLUMN IF EXISTS oceanengine_creative_id,
    DROP COLUMN IF EXISTS oceanengine_ad_id,
    DROP COLUMN IF EXISTS oceanengine_click_id;
