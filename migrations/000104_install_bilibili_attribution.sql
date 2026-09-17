-- +goose Up
ALTER TABLE install_tokens
    ADD COLUMN IF NOT EXISTS bilibili_track_id TEXT NOT NULL DEFAULT '',
    ADD COLUMN IF NOT EXISTS bilibili_form_submit_code INT NOT NULL DEFAULT -1,
    ADD COLUMN IF NOT EXISTS bilibili_form_submit_sent_at BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS bilibili_clue_valid_code INT NOT NULL DEFAULT -1,
    ADD COLUMN IF NOT EXISTS bilibili_clue_valid_sent_at BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE install_tokens
    DROP COLUMN IF EXISTS bilibili_clue_valid_sent_at,
    DROP COLUMN IF EXISTS bilibili_clue_valid_code,
    DROP COLUMN IF EXISTS bilibili_form_submit_sent_at,
    DROP COLUMN IF EXISTS bilibili_form_submit_code,
    DROP COLUMN IF EXISTS bilibili_track_id;
