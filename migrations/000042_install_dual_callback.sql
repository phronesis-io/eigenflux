-- +goose Up
-- +goose StatementBegin
-- Two-stage 聚光 ocpx conversion, tracked independently so each fires exactly
-- once: event_type 101 on the copy click (shallow intent), event_type 102 on the
-- successful install (deep conversion). copied_at is the copy-stage funnel signal.
-- cbNNN_code: -1 not attempted, 0 accepted (terminal), >0 platform error, -2
-- transport error (non-zero is re-claimable). Legacy callback_code/callback_sent_at
-- are left in place (superseded by the per-event columns).
ALTER TABLE install_tokens ADD COLUMN copied_at    BIGINT NOT NULL DEFAULT 0;
ALTER TABLE install_tokens ADD COLUMN cb101_code   INT    NOT NULL DEFAULT -1;
ALTER TABLE install_tokens ADD COLUMN cb101_sent_at BIGINT NOT NULL DEFAULT 0;
ALTER TABLE install_tokens ADD COLUMN cb102_code   INT    NOT NULL DEFAULT -1;
ALTER TABLE install_tokens ADD COLUMN cb102_sent_at BIGINT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE install_tokens DROP COLUMN copied_at;
ALTER TABLE install_tokens DROP COLUMN cb101_code;
ALTER TABLE install_tokens DROP COLUMN cb101_sent_at;
ALTER TABLE install_tokens DROP COLUMN cb102_code;
ALTER TABLE install_tokens DROP COLUMN cb102_sent_at;
-- +goose StatementEnd
