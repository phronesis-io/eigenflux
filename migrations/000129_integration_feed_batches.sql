-- +goose Up
ALTER TABLE integration_apps ADD COLUMN max_feed_batches_per_day INTEGER NOT NULL DEFAULT 10000 CHECK (max_feed_batches_per_day > 0);
ALTER TABLE integration_apps ADD COLUMN feed_usage_day BIGINT NOT NULL DEFAULT 0;
ALTER TABLE integration_apps ADD COLUMN feed_batches_today BIGINT NOT NULL DEFAULT 0;
ALTER TABLE integration_apps ADD COLUMN feed_batches_total BIGINT NOT NULL DEFAULT 0;

-- One durable latest batch per Agent. Monotonic sequences reject old retries
-- even after their payload has been replaced; an expired cache never regenerates.
CREATE TABLE integration_feed_batches (
    agent_id BIGINT PRIMARY KEY REFERENCES integration_agents(agent_id),
    sequence BIGINT NOT NULL CHECK (sequence > 0),
    batch_id UUID NOT NULL UNIQUE,
    request_hash VARCHAR(64) NOT NULL,
    status TEXT NOT NULL CHECK (status IN ('generating','ready','unknown')),
    matching_version BIGINT NOT NULL CHECK (matching_version > 0),
    payload JSONB NOT NULL DEFAULT '{}',
    item_count INTEGER NOT NULL DEFAULT 0,
    lease_until BIGINT NOT NULL,
    created_at BIGINT NOT NULL,
    completed_at BIGINT NOT NULL DEFAULT 0,
    next_poll_at BIGINT NOT NULL,
    failure_code TEXT NOT NULL DEFAULT ''
);

-- +goose Down
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS(SELECT 1 FROM integration_feed_batches) THEN
        RAISE EXCEPTION 'integration Feed batches exist; preserve sequence fences before rollback';
    END IF;
END $$;
-- +goose StatementEnd
DROP TABLE integration_feed_batches;
ALTER TABLE integration_apps DROP COLUMN max_feed_batches_per_day;
ALTER TABLE integration_apps DROP COLUMN feed_usage_day;
ALTER TABLE integration_apps DROP COLUMN feed_batches_today;
ALTER TABLE integration_apps DROP COLUMN feed_batches_total;
