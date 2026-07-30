-- +goose Up
-- +goose StatementBegin
CREATE TABLE xingtu_clicks (id VARCHAR(64) PRIMARY KEY, callback_param TEXT NOT NULL DEFAULT '', demand_id VARCHAR(128) NOT NULL DEFAULT '', item_id VARCHAR(128) NOT NULL DEFAULT '', created_at BIGINT NOT NULL, bound_token VARCHAR(32) NOT NULL DEFAULT '');
ALTER TABLE install_tokens ADD COLUMN xingtu_callback TEXT NOT NULL DEFAULT '';
ALTER TABLE install_tokens ADD COLUMN xingtu_cb_activate_code INT NOT NULL DEFAULT -1;
ALTER TABLE install_tokens ADD COLUMN xingtu_cb_activate_sent_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE install_tokens ADD COLUMN xingtu_cb_register_code INT NOT NULL DEFAULT -1;
ALTER TABLE install_tokens ADD COLUMN xingtu_cb_register_sent_at BIGINT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE install_tokens DROP COLUMN xingtu_cb_register_sent_at;
ALTER TABLE install_tokens DROP COLUMN xingtu_cb_register_code;
ALTER TABLE install_tokens DROP COLUMN xingtu_cb_activate_sent_at;
ALTER TABLE install_tokens DROP COLUMN xingtu_cb_activate_code;
ALTER TABLE install_tokens DROP COLUMN xingtu_callback;
DROP TABLE xingtu_clicks;
-- +goose StatementEnd
