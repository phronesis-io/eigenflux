-- +goose Up
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_feedback_health_daily
 ADD COLUMN batches_handed BIGINT NOT NULL DEFAULT 0 CHECK (batches_handed BETWEEN 0 AND 1000000),
 ADD COLUMN local_polls BIGINT NOT NULL DEFAULT 0 CHECK (local_polls BETWEEN 0 AND 1000000),
 ADD COLUMN rehanded_items BIGINT NOT NULL DEFAULT 0 CHECK (rehanded_items BETWEEN 0 AND 1000000),
 ADD COLUMN backlog_expired_items BIGINT NOT NULL DEFAULT 0 CHECK (backlog_expired_items BETWEEN 0 AND 1000000),
 ADD COLUMN surface_reminders BIGINT NOT NULL DEFAULT 0 CHECK (surface_reminders BETWEEN 0 AND 1000000),
 ADD COLUMN detail_fetches BIGINT NOT NULL DEFAULT 0 CHECK (detail_fetches BETWEEN 0 AND 1000000),
 ADD COLUMN detail_fetch_failures BIGINT NOT NULL DEFAULT 0 CHECK (detail_fetch_failures BETWEEN 0 AND 1000000),
 ADD COLUMN oversize_batches BIGINT NOT NULL DEFAULT 0 CHECK (oversize_batches BETWEEN 0 AND 1000000),
 ADD COLUMN overlapping_cycles BIGINT NOT NULL DEFAULT 0 CHECK (overlapping_cycles BETWEEN 0 AND 1000000),
 ADD COLUMN batch_body_bytes BIGINT NOT NULL DEFAULT 0 CHECK (batch_body_bytes BETWEEN 0 AND 1099511627776),
 ADD COLUMN batch_body_max_bytes BIGINT NOT NULL DEFAULT 0 CHECK (batch_body_max_bytes BETWEEN 0 AND 1099511627776),
 ADD CONSTRAINT feed_health_byte_max CHECK (batch_body_max_bytes <= batch_body_bytes);

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE agent_feedback_health_daily
 DROP CONSTRAINT feed_health_byte_max,
 DROP COLUMN batches_handed,
 DROP COLUMN local_polls,
 DROP COLUMN rehanded_items,
 DROP COLUMN backlog_expired_items,
 DROP COLUMN surface_reminders,
 DROP COLUMN detail_fetches,
 DROP COLUMN detail_fetch_failures,
 DROP COLUMN oversize_batches,
 DROP COLUMN overlapping_cycles,
 DROP COLUMN batch_body_bytes,
 DROP COLUMN batch_body_max_bytes;
