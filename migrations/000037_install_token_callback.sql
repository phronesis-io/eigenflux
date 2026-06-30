-- +goose Up
-- +goose StatementBegin
-- callback_sent_at: set the first (and only) time a platform conversion callback
-- is sent for this ref (Loop B). Guards idempotency so the same click is never
-- reported to the ad platform's optimizer twice, whether the trigger was the
-- /r/<ref> fetch (the in-window proxy conversion) or the later install report.
ALTER TABLE install_tokens ADD COLUMN callback_sent_at BIGINT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE install_tokens DROP COLUMN callback_sent_at;
-- +goose StatementEnd
