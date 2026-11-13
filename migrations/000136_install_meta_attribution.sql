-- +goose Up
ALTER TABLE install_tokens
    ADD COLUMN meta_fbc TEXT NOT NULL DEFAULT '',
    ADD COLUMN meta_fbp TEXT NOT NULL DEFAULT '',
    ADD COLUMN meta_user_agent TEXT NOT NULL DEFAULT '',
    ADD COLUMN meta_copy_code INT NOT NULL DEFAULT -1,
    ADD COLUMN meta_copy_sent_at BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN meta_provisioned_at BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN meta_install_code INT NOT NULL DEFAULT -1,
    ADD COLUMN meta_install_sent_at BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE install_tokens
    DROP COLUMN meta_install_sent_at,
    DROP COLUMN meta_install_code,
    DROP COLUMN meta_provisioned_at,
    DROP COLUMN meta_copy_sent_at,
    DROP COLUMN meta_copy_code,
    DROP COLUMN meta_user_agent,
    DROP COLUMN meta_fbp,
    DROP COLUMN meta_fbc;
