-- Agent Card Periodic Profile Refresh health metrics.
-- Definitions: docs/metrics/agent-card-refresh.md
--
-- Run with psql -X -v ON_ERROR_STOP=1 -v week_start='2026-10-05' \
--   -f scripts/diagnostics/agent_card_refresh_metrics.sql
-- week_start is a Monday in Asia/Shanghai; the week is [week_start, week_start + 7 days).
-- Run it soon after the week ends: the active-agent denominator uses
-- agent_settings.last_activity_at, which only keeps the latest activity.
-- Outputs are aggregates or agent IDs/paths; profile values are never printed.
BEGIN ISOLATION LEVEL REPEATABLE READ READ ONLY;
SET LOCAL statement_timeout = '60s';
SET LOCAL lock_timeout = '2s';
SET LOCAL TIME ZONE 'Asia/Shanghai';

SELECT (extract(epoch FROM :'week_start'::date::timestamptz) * 1000)::bigint AS week_start_ms,
       (extract(epoch FROM (:'week_start'::date + 7)::timestamptz) * 1000)::bigint AS week_end_ms,
       (extract(epoch FROM (:'week_start'::date - 23)::timestamptz) * 1000)::bigint AS stale_cutoff_ms,
       (extract(epoch FROM (:'week_start'::date - 7)::timestamptz) * 1000)::bigint AS flip_lookback_ms
\gset

-- Every query repeats the cohort CTE because the transaction is read-only:
-- active onboarded agents = Console V2 onboarding completed, last successful
-- Agent request at or after week start, created before week end, excluding
-- internal PGC/bot accounts.
-- Need fields: seeking, offering, current_focus, demands, agent_status, human_status.

-- ── Layer 1a: weekly run rate ───────────────────────────────────────────────
-- auto_run_rate: share of the cohort with >= 1 completed run (changed or
-- unchanged) dispatched by the CLI schedule (plugin_task / pending_line).
-- any_run_rate also counts manual_force and untracked completions.
WITH cohort AS (
    SELECT a.agent_id
    FROM agents a
    JOIN agent_onboarding_v2 o ON o.agent_id = a.agent_id AND o.state = 'completed'
    JOIN agent_settings s ON s.agent_id = a.agent_id
    WHERE s.last_activity_at >= :week_start_ms
      AND a.created_at < :week_end_ms
      AND COALESCE(a.email, '') NOT LIKE '%@pgc.eigenflux.one'
      AND COALESCE(a.email, '') NOT LIKE '%@bot.eigenflux.one'
), per_agent AS (
    SELECT c.agent_id,
           count(r.id) FILTER (WHERE r.stage = 'completed' AND r.trigger IN ('plugin_task', 'pending_line')) AS auto_runs,
           count(r.id) FILTER (WHERE r.stage = 'completed') AS any_runs,
           count(r.id) FILTER (WHERE r.stage = 'dispatched') AS dispatches,
           count(r.id) AS reports
    FROM cohort c
    LEFT JOIN agent_profile_refresh_runs r
      ON r.agent_id = c.agent_id AND r.created_at >= :week_start_ms AND r.created_at < :week_end_ms
    GROUP BY c.agent_id
)
SELECT :'week_start'::date AS week_start,
       count(*) AS active_onboarded_agents,
       count(*) FILTER (WHERE auto_runs > 0) AS agents_with_auto_run,
       round(count(*) FILTER (WHERE auto_runs > 0)::numeric / NULLIF(count(*), 0), 4) AS auto_run_rate,
       count(*) FILTER (WHERE any_runs > 0) AS agents_with_any_run,
       round(count(*) FILTER (WHERE any_runs > 0)::numeric / NULLIF(count(*), 0), 4) AS any_run_rate,
       count(*) FILTER (WHERE dispatches > 0) AS agents_with_dispatch,
       count(*) FILTER (WHERE reports = 0) AS agents_without_any_report
FROM per_agent;

-- Run rate by the latest reported integration mode and CLI version in the
-- week. Agents without any report fall under '(none)'.
WITH cohort AS (
    SELECT a.agent_id
    FROM agents a
    JOIN agent_onboarding_v2 o ON o.agent_id = a.agent_id AND o.state = 'completed'
    JOIN agent_settings s ON s.agent_id = a.agent_id
    WHERE s.last_activity_at >= :week_start_ms
      AND a.created_at < :week_end_ms
      AND COALESCE(a.email, '') NOT LIKE '%@pgc.eigenflux.one'
      AND COALESCE(a.email, '') NOT LIKE '%@bot.eigenflux.one'
)
SELECT CASE WHEN latest.agent_id IS NULL THEN '(none)' ELSE COALESCE(NULLIF(latest.client_mode, ''), '(unknown)') END AS client_mode,
       COALESCE(NULLIF(latest.cli_version, ''), '(none)') AS cli_version,
       count(*) AS active_onboarded_agents,
       count(*) FILTER (WHERE EXISTS (
           SELECT 1 FROM agent_profile_refresh_runs r
           WHERE r.agent_id = c.agent_id AND r.stage = 'completed'
             AND r.trigger IN ('plugin_task', 'pending_line')
             AND r.created_at >= :week_start_ms AND r.created_at < :week_end_ms
       )) AS agents_with_auto_run
FROM cohort c
LEFT JOIN LATERAL (
    SELECT r.agent_id, r.client_mode, r.cli_version
    FROM agent_profile_refresh_runs r
    WHERE r.agent_id = c.agent_id
      AND r.created_at >= :week_start_ms AND r.created_at < :week_end_ms
    ORDER BY r.created_at DESC, r.id DESC
    LIMIT 1
) latest ON TRUE
GROUP BY 1, 2
ORDER BY 3 DESC, 1, 2;

-- ── Layer 1b: 24h failure rate ──────────────────────────────────────────────
-- Dispatches in the week (all agents) that have had 24h to mature. A dispatch
-- fails when no completion with the same (agent_id, run_id) arrived within 24h.
-- The NULL trigger row is the overall total.
WITH dispatches AS (
    SELECT d.agent_id, d.run_id, d.trigger, COALESCE(NULLIF(d.client_mode, ''), '(unknown)') AS client_mode, d.created_at
    FROM agent_profile_refresh_runs d
    WHERE d.stage = 'dispatched'
      AND d.created_at >= :week_start_ms AND d.created_at < :week_end_ms
      AND d.created_at <= (extract(epoch FROM now()) * 1000)::bigint - 86400000
), outcomes AS (
    SELECT d.*,
           EXISTS (
               SELECT 1 FROM agent_profile_refresh_runs c
               WHERE c.agent_id = d.agent_id AND c.run_id = d.run_id AND c.stage = 'completed'
                 AND c.created_at <= d.created_at + 86400000
           ) AS completed_24h
    FROM dispatches d
)
SELECT trigger,
       client_mode,
       count(*) AS mature_dispatches,
       count(*) FILTER (WHERE NOT completed_24h) AS failed_24h,
       round(count(*) FILTER (WHERE NOT completed_24h)::numeric / NULLIF(count(*), 0), 4) AS failure_rate_24h,
       count(DISTINCT agent_id) AS agents
FROM outcomes
GROUP BY ROLLUP (trigger, client_mode)
ORDER BY trigger NULLS FIRST, client_mode NULLS FIRST;

-- ── Layer 2a: weekly need-field change counts ───────────────────────────────
-- One row per agent with need-field changes in the week (any actor/source).
-- path_changes counts (event, path) pairs; high_churn flags >= 5.
WITH per_path AS (
    SELECT e.agent_id, p.path,
           count(*) AS changes,
           count(*) FILTER (WHERE e.source = 'cli_daily_refresh') AS daily_refresh_changes
    FROM agent_profile_change_events e
    CROSS JOIN LATERAL jsonb_array_elements_text(e.changed_paths) AS p(path)
    WHERE e.created_at >= :week_start_ms AND e.created_at < :week_end_ms
      AND p.path IN ('seeking', 'offering', 'current_focus', 'demands', 'agent_status', 'human_status')
    GROUP BY e.agent_id, p.path
)
SELECT agent_id,
       sum(changes) AS path_changes,
       sum(daily_refresh_changes) AS daily_refresh_changes,
       jsonb_object_agg(path, changes ORDER BY path) AS changes_by_path,
       sum(changes) >= 5 AS high_churn
FROM per_path
GROUP BY agent_id
ORDER BY path_changes DESC, agent_id;

-- Cohort distribution, agents with zero changes included.
WITH cohort AS (
    SELECT a.agent_id
    FROM agents a
    JOIN agent_onboarding_v2 o ON o.agent_id = a.agent_id AND o.state = 'completed'
    JOIN agent_settings s ON s.agent_id = a.agent_id
    WHERE s.last_activity_at >= :week_start_ms
      AND a.created_at < :week_end_ms
      AND COALESCE(a.email, '') NOT LIKE '%@pgc.eigenflux.one'
      AND COALESCE(a.email, '') NOT LIKE '%@bot.eigenflux.one'
), per_agent AS (
    SELECT c.agent_id,
           (SELECT count(*)
            FROM agent_profile_change_events e
            CROSS JOIN LATERAL jsonb_array_elements_text(e.changed_paths) AS p(path)
            WHERE e.agent_id = c.agent_id
              AND e.created_at >= :week_start_ms AND e.created_at < :week_end_ms
              AND p.path IN ('seeking', 'offering', 'current_focus', 'demands', 'agent_status', 'human_status')
           ) AS path_changes
    FROM cohort c
)
SELECT count(*) AS active_onboarded_agents,
       count(*) FILTER (WHERE path_changes = 0) AS no_change,
       count(*) FILTER (WHERE path_changes BETWEEN 1 AND 4) AS changed_1_to_4,
       count(*) FILTER (WHERE path_changes >= 5) AS high_churn_5_plus,
       percentile_cont(0.5) WITHIN GROUP (ORDER BY path_changes) AS median_changes,
       max(path_changes) AS max_changes
FROM per_agent;

-- ── Layer 2b: active but no need-field change in 30 days ────────────────────
-- Cohort agents whose newest need-field change (any actor) is older than 30
-- days before week end, or who never changed one. Cleanup always keeps the
-- newest event per field, so this stays correct past the 90-day retention.
WITH cohort AS (
    SELECT a.agent_id
    FROM agents a
    JOIN agent_onboarding_v2 o ON o.agent_id = a.agent_id AND o.state = 'completed'
    JOIN agent_settings s ON s.agent_id = a.agent_id
    WHERE s.last_activity_at >= :week_start_ms
      AND a.created_at < :week_end_ms
      AND COALESCE(a.email, '') NOT LIKE '%@pgc.eigenflux.one'
      AND COALESCE(a.email, '') NOT LIKE '%@bot.eigenflux.one'
)
SELECT c.agent_id,
       to_timestamp(last_change.at / 1000.0) AS last_need_change_at,
       EXISTS (
           SELECT 1 FROM agent_profile_refresh_runs r
           WHERE r.agent_id = c.agent_id AND r.stage = 'completed'
             AND r.created_at >= :stale_cutoff_ms AND r.created_at < :week_end_ms
       ) AS completed_refresh_in_30d
FROM cohort c
LEFT JOIN LATERAL (
    SELECT max(e.created_at) AS at
    FROM agent_profile_change_events e
    WHERE e.agent_id = c.agent_id
      AND e.created_at < :week_end_ms
      AND e.changed_paths ?| ARRAY['seeking', 'offering', 'current_focus', 'demands', 'agent_status', 'human_status']
) last_change ON TRUE
WHERE last_change.at IS NULL OR last_change.at < :stale_cutoff_ms
ORDER BY last_change.at NULLS FIRST, c.agent_id;

-- ── Layer 2c: flip-backs within 7 days ──────────────────────────────────────
-- A need-field change in the week whose new value equals the value the field
-- had before an earlier change of the same field at most 7 days before
-- (A -> B -> A). JSONB equality ignores key order and whitespace.
WITH need_changes AS (
    SELECT e.id, e.agent_id, e.created_at, e.actor_type, e.source, p.path,
           e.previous_values -> p.path AS before_value,
           e.new_values -> p.path AS after_value
    FROM agent_profile_change_events e
    CROSS JOIN LATERAL jsonb_array_elements_text(e.changed_paths) AS p(path)
    WHERE e.created_at >= :flip_lookback_ms AND e.created_at < :week_end_ms
      AND p.path IN ('seeking', 'offering', 'current_focus', 'demands', 'agent_status', 'human_status')
)
SELECT later.agent_id,
       later.path,
       to_timestamp(earlier.created_at / 1000.0) AS first_change_at,
       to_timestamp(later.created_at / 1000.0) AS reverted_at,
       earlier.actor_type || '/' || earlier.source AS first_change_by,
       later.actor_type || '/' || later.source AS reverted_by
FROM need_changes later
JOIN need_changes earlier
  ON earlier.agent_id = later.agent_id
 AND earlier.path = later.path
 AND (earlier.created_at, earlier.id) < (later.created_at, later.id)
 AND earlier.created_at >= later.created_at - 7::bigint * 86400000
WHERE later.created_at >= :week_start_ms
  AND earlier.before_value IS NOT NULL
  AND later.after_value = earlier.before_value
ORDER BY later.agent_id, later.path, later.created_at;

ROLLBACK;
