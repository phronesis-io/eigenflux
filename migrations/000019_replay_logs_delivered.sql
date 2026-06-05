-- +goose Up
-- +goose StatementBegin
-- Marks whether a replay row was actually delivered to the agent (TRUE) or
-- only scored and filtered out below threshold (FALSE). Nullable with no
-- default: rows written before this column (and events from pre-upgrade feed
-- binaries during a rolling deploy) stay NULL and are excluded from the beat
-- coverage "pushed" counter — undercounting is preferred over miscounting.
ALTER TABLE replay_logs ADD COLUMN delivered BOOLEAN;
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
ALTER TABLE replay_logs DROP COLUMN IF EXISTS delivered;
-- +goose StatementEnd
