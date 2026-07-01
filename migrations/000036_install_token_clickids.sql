-- +goose Up
-- +goose StatementBegin
-- Platform click identifiers captured from the landing URL at mint time, kept so
-- a later (cross-device, delayed) install can be reported back to the ad
-- platform's optimizer keyed by the original click. click_id = Xiaohongshu 聚光
-- (appended as ?click_id=); twclid = X (Twitter) Ads. Exactly one is set for
-- paid traffic; both empty for organic/direct.
ALTER TABLE install_tokens ADD COLUMN click_id VARCHAR(128) NOT NULL DEFAULT '';
ALTER TABLE install_tokens ADD COLUMN twclid   VARCHAR(128) NOT NULL DEFAULT '';
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE install_tokens DROP COLUMN click_id;
ALTER TABLE install_tokens DROP COLUMN twclid;
-- +goose StatementEnd
