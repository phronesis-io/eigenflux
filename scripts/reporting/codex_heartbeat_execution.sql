-- Read-only Codex D1 diagnosis for the latest seven mature registration days.
-- Current runtime attribution; Asia/Shanghai natural days; no missing cohort rows.
-- Counts describe observed evidence, never proof of an absent scheduler trigger.
-- Query 1: per-cycle D1 observations. A cycle crossing midnight can have partial
-- receipts in this window; this is not a final all-time outcome for that cycle.
WITH params AS (
  SELECT (now() AT TIME ZONE 'Asia/Shanghai')::date AS today,
         (extract(epoch FROM now()) * 1000)::bigint AS queried_at_ms
), bounds AS (
  SELECT p.*,
         (extract(epoch FROM ((today - 8)::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint AS first_registration_ms,
         (extract(epoch FROM ((today - 1)::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint AS end_registration_ms,
         (extract(epoch FROM ((today - 7)::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint AS first_d1_ms,
         (extract(epoch FROM (today::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint AS end_d1_ms
  FROM params p
), cohort AS (
  SELECT a.agent_id,
         (to_timestamp(a.created_at / 1000.0) AT TIME ZONE 'Asia/Shanghai')::date AS registration_day,
         (to_timestamp(a.created_at / 1000.0) AT TIME ZONE 'Asia/Shanghai')::date + 1 AS activity_day
  FROM agents a
  LEFT JOIN agent_settings s ON s.agent_id = a.agent_id
  CROSS JOIN bounds b
  WHERE a.created_at >= b.first_registration_ms AND a.created_at < b.end_registration_ms
    AND a.identity_state = 'active' AND a.email_kind IN ('legacy_real', 'v2_bound')
    AND NOT coalesce(a.is_official, false)
    AND a.email NOT LIKE '%bot.eigenflux%' AND a.email NOT LIKE '%pgc.eigenflux%'
    AND coalesce(nullif(lower(btrim(s.runtime_name)), ''),
                 nullif(lower(btrim(split_part(s.client_host, '/', 1))), ''), 'unknown') = 'codex'
), observations AS (
  SELECT c.agent_id, c.registration_day, c.activity_day,
         t.properties->>'attempt_id' AS attempt_id,
         t.properties->>'phase' AS phase,
         t.properties->>'result' AS result,
         t.properties->'runtime'->>'stage' AS stage,
         t.properties->'runtime'->>'plan_mode' AS plan_mode,
         t.event_at
  FROM cohort c
  JOIN telemetry_events_v2 t ON t.agent_id = c.agent_id
  CROSS JOIN bounds b
  WHERE t.event_type = 'maintenance_attempt'
    AND t.event_at >= b.first_d1_ms AND t.event_at < b.end_d1_ms
    AND t.created_at >= b.first_d1_ms - 300000 AND t.created_at <= b.queried_at_ms
    AND t.event_at >= (extract(epoch FROM (c.activity_day::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint
    AND t.event_at < (extract(epoch FROM ((c.activity_day + 1)::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint
    AND t.properties->>'component' = 'runtime'
    AND t.properties->>'phase' IN ('cycle', 'lifecycle')
), cycles AS (
  SELECT agent_id, registration_day, activity_day, attempt_id,
         min(event_at) AS first_observed_at_ms, max(event_at) AS last_observed_at_ms,
         bool_or(result = 'plan_created' AND stage = 'cycle') AS plan_created,
         bool_or(result = 'started' AND stage = 'cycle') AS execution_started,
         bool_or(stage <> 'cycle' AND result IN ('completed', 'failed')) AS stage_execution_observed,
         bool_or(result = 'completed' AND stage = 'cycle') AS completed,
         bool_or(result = 'failed' AND stage = 'cycle') AS failed,
         bool_or(result = 'incomplete' AND stage = 'cycle') AS incomplete,
         bool_or(result = 'permission_required' AND stage = 'cycle') AS permission_required,
         bool_or(result = 'interrupted' AND stage = 'cycle') AS interrupted
  FROM observations WHERE phase = 'cycle'
  GROUP BY 1, 2, 3, 4
)
SELECT c.agent_id, c.registration_day, c.activity_day, x.attempt_id,
       x.first_observed_at_ms, x.last_observed_at_ms,
       x.plan_created, x.execution_started, x.stage_execution_observed,
       x.completed, x.failed, x.incomplete, x.permission_required, x.interrupted,
       CASE WHEN x.completed THEN 'completed'
            WHEN x.failed THEN 'failed'
            WHEN x.permission_required THEN 'permission_request_observed'
            WHEN x.interrupted THEN 'interruption_observed'
            WHEN x.plan_created AND NOT x.execution_started AND NOT x.stage_execution_observed
              THEN 'planned_without_execution_evidence'
            WHEN x.execution_started OR x.stage_execution_observed THEN 'execution_observed'
            WHEN x.incomplete THEN 'incomplete_observed'
            ELSE 'instrumentation_unknown' END AS observation_label
FROM cohort c LEFT JOIN cycles x USING (agent_id, registration_day, activity_day)
ORDER BY c.registration_day, c.agent_id, x.first_observed_at_ms;

-- Query 2: per-Agent D1 evidence, including Agents with no instrumentation.
WITH params AS (
  SELECT (now() AT TIME ZONE 'Asia/Shanghai')::date AS today,
         (extract(epoch FROM now()) * 1000)::bigint AS queried_at_ms
), bounds AS (
  SELECT p.*,
         (extract(epoch FROM ((today - 8)::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint AS first_registration_ms,
         (extract(epoch FROM ((today - 1)::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint AS end_registration_ms,
         (extract(epoch FROM ((today - 7)::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint AS first_d1_ms,
         (extract(epoch FROM (today::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint AS end_d1_ms
  FROM params p
), cohort AS (
  SELECT a.agent_id,
         (to_timestamp(a.created_at / 1000.0) AT TIME ZONE 'Asia/Shanghai')::date AS registration_day,
         (to_timestamp(a.created_at / 1000.0) AT TIME ZONE 'Asia/Shanghai')::date + 1 AS activity_day
  FROM agents a
  LEFT JOIN agent_settings s ON s.agent_id = a.agent_id
  CROSS JOIN bounds b
  WHERE a.created_at >= b.first_registration_ms AND a.created_at < b.end_registration_ms
    AND a.identity_state = 'active' AND a.email_kind IN ('legacy_real', 'v2_bound')
    AND NOT coalesce(a.is_official, false)
    AND a.email NOT LIKE '%bot.eigenflux%' AND a.email NOT LIKE '%pgc.eigenflux%'
    AND coalesce(nullif(lower(btrim(s.runtime_name)), ''),
                 nullif(lower(btrim(split_part(s.client_host, '/', 1))), ''), 'unknown') = 'codex'
), observations AS (
  SELECT c.agent_id, c.registration_day, c.activity_day,
         t.properties->>'attempt_id' AS attempt_id,
         t.properties->>'phase' AS phase,
         t.properties->>'result' AS result,
         t.properties->'runtime'->>'stage' AS stage,
         t.properties->'runtime'->>'plan_mode' AS plan_mode,
         t.event_at
  FROM cohort c
  JOIN telemetry_events_v2 t ON t.agent_id = c.agent_id
  CROSS JOIN bounds b
  WHERE t.event_type = 'maintenance_attempt'
    AND t.event_at >= b.first_d1_ms AND t.event_at < b.end_d1_ms
    AND t.created_at >= b.first_d1_ms - 300000 AND t.created_at <= b.queried_at_ms
    AND t.event_at >= (extract(epoch FROM (c.activity_day::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint
    AND t.event_at < (extract(epoch FROM ((c.activity_day + 1)::timestamp AT TIME ZONE 'Asia/Shanghai')) * 1000)::bigint
    AND t.properties->>'component' = 'runtime'
    AND t.properties->>'phase' IN ('cycle', 'lifecycle')
), daily AS (
  SELECT c.agent_id, c.registration_day, c.activity_day,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'lifecycle' AND stage = 'trigger' AND result = 'observed') AS trigger_observed,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'cycle' AND stage = 'cycle' AND result = 'plan_created') AS plan_created,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'cycle' AND stage = 'cycle' AND result = 'started') AS execution_started,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'cycle' AND stage = 'cycle' AND result = 'completed') AS completed,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'cycle' AND stage = 'cycle' AND result = 'failed') AS failed,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'cycle' AND stage = 'cycle' AND result = 'incomplete') AS incomplete,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'cycle' AND stage = 'cycle' AND result = 'permission_required') AS cycle_permission_required,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'cycle' AND stage = 'cycle' AND result = 'interrupted') AS cycle_interrupted,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'lifecycle' AND stage = 'permission' AND result = 'required') AS permission_required,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'lifecycle' AND stage = 'permission' AND result = 'forbidden') AS policy_forbidden,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'lifecycle' AND stage = 'permission' AND result = 'legacy_mismatch') AS policy_legacy_mismatch,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'lifecycle' AND stage = 'guard' AND result = 'configured') AS guard_configured,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'lifecycle' AND stage = 'guard' AND result = 'active') AS guard_active,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'lifecycle' AND stage = 'execution' AND result = 'interrupted') AS execution_interrupted,
         count(DISTINCT o.attempt_id) FILTER (WHERE phase = 'cycle' AND stage <> 'cycle' AND result IN ('completed', 'failed')) AS stage_execution
  FROM cohort c LEFT JOIN observations o USING (agent_id, registration_day, activity_day)
  GROUP BY 1, 2, 3
)
SELECT d.*,
       CASE WHEN completed > 0 THEN 'completed'
            WHEN failed > 0 THEN 'failed'
            WHEN cycle_permission_required > 0 OR permission_required > 0 THEN 'permission_request_observed'
            WHEN cycle_interrupted > 0 OR execution_interrupted > 0 THEN 'interruption_observed'
            WHEN plan_created > 0 AND execution_started = 0 AND stage_execution = 0
              THEN 'planned_without_execution_evidence'
            WHEN execution_started > 0 OR stage_execution > 0 THEN 'execution_observed'
            WHEN incomplete > 0 THEN 'incomplete_observed'
            WHEN trigger_observed > 0 THEN 'trigger_observed_without_plan_evidence'
            WHEN guard_active > 0 THEN 'no_trigger_observed'
            ELSE 'instrumentation_unknown' END AS observation_label
FROM daily d ORDER BY registration_day, agent_id;
