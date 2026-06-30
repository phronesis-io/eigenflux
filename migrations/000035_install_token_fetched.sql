-- +goose Up
-- +goose StatementBegin
-- fetched_at: set the first time an agent fetches /r/<ref> (reads the join
-- bootstrap). It is the earliest post-click signal — instructions read but not
-- yet installed — used as the proxy conversion fed to ad platforms inside their
-- short attribution windows, ahead of the later (cross-device) install report.
ALTER TABLE install_tokens ADD COLUMN fetched_at BIGINT NOT NULL DEFAULT 0;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE install_tokens DROP COLUMN fetched_at;
-- +goose StatementEnd
