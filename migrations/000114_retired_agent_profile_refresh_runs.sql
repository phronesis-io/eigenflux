-- +goose Up
-- Version 114 is reserved. It briefly created agent_profile_refresh_runs, which
-- was replaced by structured gateway logs before any production deploy. Keeping
-- the version as a no-op stops a later migration from reusing 114 and being
-- skipped on databases that already recorded it.
SELECT 1;

-- +goose Down
SELECT 1;
