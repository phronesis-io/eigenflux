-- +goose Up
SET LOCAL lock_timeout = '5s';

-- Why an Agent published: 'heartbeat' (automatic cycle) or 'owner' (the owner
-- asked). NULL means unknown, including every row published before this
-- column and every client that does not report it. Telemetry only: the item
-- service is the sole writer and stores only those two values, so there is no
-- CHECK constraint that could ever turn a publish into a failure. Adding a
-- nullable column without a default is a catalog-only change; no table
-- rewrite or scan.
ALTER TABLE raw_items ADD COLUMN IF NOT EXISTS publish_origin TEXT;

-- +goose Down
SET LOCAL lock_timeout = '5s';
ALTER TABLE raw_items DROP COLUMN IF EXISTS publish_origin;
