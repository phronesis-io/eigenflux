-- +goose Up
-- +goose StatementBegin
-- Per-event X Ads CAPI callback state for the server-confirmed token-minted
-- and command-copied funnel stages. Codes: -1 not attempted, 0 accepted,
-- >0 X HTTP error, -2 transport/signing error.
ALTER TABLE install_tokens ADD COLUMN x_cb_token_code INT NOT NULL DEFAULT -1;
ALTER TABLE install_tokens ADD COLUMN x_cb_token_sent_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE install_tokens ADD COLUMN x_cb_copy_code INT NOT NULL DEFAULT -1;
ALTER TABLE install_tokens ADD COLUMN x_cb_copy_sent_at BIGINT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE install_tokens DROP COLUMN x_cb_copy_sent_at;
ALTER TABLE install_tokens DROP COLUMN x_cb_copy_code;
ALTER TABLE install_tokens DROP COLUMN x_cb_token_sent_at;
ALTER TABLE install_tokens DROP COLUMN x_cb_token_code;
-- +goose StatementEnd
