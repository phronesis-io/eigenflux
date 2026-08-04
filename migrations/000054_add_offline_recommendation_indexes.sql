-- +goose NO TRANSACTION

-- +goose Up
-- Both source tables are append-only and write-heavy. Build the offline scan
-- indexes without blocking replay or follow-up ingestion while PostgreSQL
-- scans existing rows.
SET lock_timeout = '5s';
SET statement_timeout = '30min';

-- Supports cross-agent daily training exports and replay-log retention cleanup.
CREATE INDEX CONCURRENTLY idx_replay_logs_served_at
    ON replay_logs(served_at);

-- Hot recall reads only recent surface labels. The partial index keeps the
-- write/storage cost bounded and carries item_id for an index-only aggregate.
CREATE INDEX CONCURRENTLY idx_followup_labels_surface_reported_item
    ON followup_labels(reported_at, item_id)
    WHERE kind = 'surface';

-- +goose Down
SET lock_timeout = '5s';
SET statement_timeout = '30min';

DROP INDEX CONCURRENTLY IF EXISTS idx_followup_labels_surface_reported_item;
DROP INDEX CONCURRENTLY IF EXISTS idx_replay_logs_served_at;
