-- +goose Up
-- +goose StatementBegin
-- One-off data fix for migration 000024's blanket backfill. 000024 pinned EVERY
-- pre-existing row to feed_poll_interval_user_set=true so the onboarding ramp
-- would only apply to agents registered after that deploy. That swept in agents
-- who had registered shortly before the deploy, were still on the default 300s,
-- and had never explicitly chosen an interval — they were denied the ramp they
-- should have qualified for.
--
-- Un-pin only the false positives, conservatively:
--   * user_set = true AND interval is still the default 300 (a genuine override
--     to a non-default value is left alone);
--   * the row was never written after the registration first-sync
--     (updated_at within 1h of agent_created_at_ms) — a later console/CLI edit
--     bumps updated_at and is treated as a real override, so it is excluded
--     even when the chosen value happens to be 300;
--   * the agent is still inside the 3-day ramp window, so un-pinning actually
--     restores the 3600s cadence (outside the window the ramp resolves to 300s
--     anyway, so those rows are intentionally left untouched).
UPDATE agent_settings
   SET feed_poll_interval_user_set = false
 WHERE feed_poll_interval_user_set = true
   AND feed_poll_interval = 300
   AND agent_created_at_ms > 0
   AND (updated_at - agent_created_at_ms) BETWEEN 0 AND 3600000
   AND agent_created_at_ms > (EXTRACT(EPOCH FROM now()) * 1000)::bigint - (3 * 24 * 60 * 60 * 1000);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
-- Re-pin the same class of rows (default 300s, untouched since first sync) to
-- restore 000024's post-state. This is best-effort: rows whose ramp window has
-- since elapsed are no longer matched by the window predicate, but for them
-- user_set is functionally irrelevant (the ramp resolves to 300s regardless),
-- so the observable cadence is unchanged either way.
UPDATE agent_settings
   SET feed_poll_interval_user_set = true
 WHERE feed_poll_interval_user_set = false
   AND feed_poll_interval = 300
   AND agent_created_at_ms > 0
   AND (updated_at - agent_created_at_ms) BETWEEN 0 AND 3600000;
-- +goose StatementEnd
