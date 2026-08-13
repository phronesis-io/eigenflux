-- +goose Up
ALTER TABLE install_tokens
    RENAME COLUMN oceanengine_cb_active_code TO oceanengine_h5_form_code;
ALTER TABLE install_tokens
    RENAME COLUMN oceanengine_cb_active_sent_at TO oceanengine_h5_form_sent_at;
ALTER TABLE install_tokens
    RENAME COLUMN oceanengine_cb_register_code TO oceanengine_h5_customer_code;
ALTER TABLE install_tokens
    RENAME COLUMN oceanengine_cb_register_sent_at TO oceanengine_h5_customer_sent_at;

UPDATE install_tokens
SET oceanengine_h5_form_code = -1,
    oceanengine_h5_form_sent_at = 0,
    oceanengine_h5_customer_code = -1,
    oceanengine_h5_customer_sent_at = 0
WHERE oceanengine_click_id <> '';

ALTER TABLE install_tokens
    ADD COLUMN IF NOT EXISTS oceanengine_omni_form_code INT NOT NULL DEFAULT -1,
    ADD COLUMN IF NOT EXISTS oceanengine_omni_form_sent_at BIGINT NOT NULL DEFAULT 0,
    ADD COLUMN IF NOT EXISTS oceanengine_omni_customer_code INT NOT NULL DEFAULT -1,
    ADD COLUMN IF NOT EXISTS oceanengine_omni_customer_sent_at BIGINT NOT NULL DEFAULT 0;

-- +goose Down
ALTER TABLE install_tokens
    DROP COLUMN IF EXISTS oceanengine_omni_customer_sent_at,
    DROP COLUMN IF EXISTS oceanengine_omni_customer_code,
    DROP COLUMN IF EXISTS oceanengine_omni_form_sent_at,
    DROP COLUMN IF EXISTS oceanengine_omni_form_code;

ALTER TABLE install_tokens
    RENAME COLUMN oceanengine_h5_form_code TO oceanengine_cb_active_code;
ALTER TABLE install_tokens
    RENAME COLUMN oceanengine_h5_form_sent_at TO oceanengine_cb_active_sent_at;
ALTER TABLE install_tokens
    RENAME COLUMN oceanengine_h5_customer_code TO oceanengine_cb_register_code;
ALTER TABLE install_tokens
    RENAME COLUMN oceanengine_h5_customer_sent_at TO oceanengine_cb_register_sent_at;

-- The lead-event success codes do not prove that the legacy activation and
-- registration events were delivered. Reopen both callbacks so the rolled-back
-- application can retry the legacy events on the next copy/report trigger.
UPDATE install_tokens
SET oceanengine_cb_active_code = -1,
    oceanengine_cb_active_sent_at = 0,
    oceanengine_cb_register_code = -1,
    oceanengine_cb_register_sent_at = 0
WHERE oceanengine_click_id <> '';
