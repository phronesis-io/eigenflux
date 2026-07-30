-- +goose Up
-- +goose StatementBegin
-- Google Ads click attribution and install-complete offline conversion state.
-- gclid is captured at landing time; code 0 is terminal success, non-zero
-- outcomes are retried after the callback lease expires.
ALTER TABLE install_tokens ADD COLUMN gclid VARCHAR(512) NOT NULL DEFAULT '';
ALTER TABLE install_tokens ADD COLUMN google_ads_cb_install_code INT NOT NULL DEFAULT -1;
ALTER TABLE install_tokens ADD COLUMN google_ads_cb_install_sent_at BIGINT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE install_tokens DROP COLUMN google_ads_cb_install_sent_at;
ALTER TABLE install_tokens DROP COLUMN google_ads_cb_install_code;
ALTER TABLE install_tokens DROP COLUMN gclid;
-- +goose StatementEnd
