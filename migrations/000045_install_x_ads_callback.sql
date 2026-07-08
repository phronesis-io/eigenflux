-- +goose Up
-- +goose StatementBegin
-- X Ads Conversion API install-success callback state, independent from the
-- Xiaohongshu cb102 columns. x_cb102_code: -1 not attempted, 0 accepted, >0 HTTP
-- or platform error, -2 transport/signing error; non-zero can be retried.
ALTER TABLE install_tokens ADD COLUMN x_cb102_code INT NOT NULL DEFAULT -1;
ALTER TABLE install_tokens ADD COLUMN x_cb102_sent_at BIGINT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE install_tokens DROP COLUMN x_cb102_code;
ALTER TABLE install_tokens DROP COLUMN x_cb102_sent_at;
-- +goose StatementEnd
