-- +goose Up
ALTER TABLE install_tokens
    ADD COLUMN meta_fbc TEXT NOT NULL DEFAULT '',
    ADD COLUMN meta_fbp TEXT NOT NULL DEFAULT '',
    ADD COLUMN meta_user_agent TEXT NOT NULL DEFAULT '',
    ADD COLUMN meta_copy_code INT NOT NULL DEFAULT -1,
    ADD COLUMN meta_copy_sent_at BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN meta_registered_at BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN meta_registration_url TEXT NOT NULL DEFAULT '',
    ADD COLUMN meta_install_code INT NOT NULL DEFAULT -1,
    ADD COLUMN meta_install_sent_at BIGINT NOT NULL DEFAULT 0;

CREATE TABLE install_meta_registration_refs (
    agent_id BIGINT PRIMARY KEY REFERENCES agents(agent_id) ON DELETE CASCADE,
    ref VARCHAR(32) NOT NULL REFERENCES install_tokens(token) ON DELETE CASCADE
);
CREATE INDEX install_meta_registration_refs_ref_idx ON install_meta_registration_refs(ref);

CREATE INDEX install_tokens_meta_pending_copy_idx ON install_tokens (copied_at)
    WHERE channel = 'facebook' AND (meta_fbc <> '' OR meta_fbp <> '') AND meta_copy_code <> 0 AND copied_at > 0;
CREATE INDEX install_tokens_meta_pending_registration_idx ON install_tokens (meta_registered_at)
    WHERE channel = 'facebook' AND (meta_fbc <> '' OR meta_fbp <> '') AND meta_install_code <> 0 AND meta_registered_at > 0;

-- +goose Down
DROP INDEX install_tokens_meta_pending_registration_idx;
DROP INDEX install_tokens_meta_pending_copy_idx;
DROP TABLE install_meta_registration_refs;
ALTER TABLE install_tokens
    DROP COLUMN meta_install_sent_at,
    DROP COLUMN meta_install_code,
    DROP COLUMN meta_registration_url,
    DROP COLUMN meta_registered_at,
    DROP COLUMN meta_copy_sent_at,
    DROP COLUMN meta_copy_code,
    DROP COLUMN meta_user_agent,
    DROP COLUMN meta_fbp,
    DROP COLUMN meta_fbc;
