-- Read-only. Supply psql variables agent_id and order_id as canonical decimals.
-- Watch records correlate local jobs/attempts; OS submission is not visibility.
SELECT to_timestamp(event_at / 1000.0) AS observed_at,
       properties->>'attempt_id' AS receipt_id,
       properties->>'platform' AS platform,
       properties->>'from_version' AS cli_version,
       properties->'notification'->>'cli_commit' AS cli_commit,
       properties->'notification'->'watch'->>'origin' AS origin,
       properties->'notification'->'watch'->>'order_id' AS order_id,
       properties->'notification'->'watch'->>'order_version' AS order_version,
       properties->'notification'->'watch'->>'role' AS role,
       properties->'notification'->'watch'->>'source_notification_id' AS source_notification_id,
       properties->'notification'->'watch'->>'job_id' AS job_id,
       properties->'notification'->'watch'->>'job_kind' AS job_kind,
       properties->'notification'->'watch'->>'job_status' AS job_status,
       properties->'notification'->'watch'->>'job_code' AS job_code,
       properties->'notification'->>'id_hash' AS notification_hash,
       properties->'notification'->>'stage' AS stage,
       properties->'notification'->'watch'->>'attempt' AS attempt,
       properties->>'result' AS result,
       properties->>'error_code' AS error_code
FROM telemetry_events_v2
WHERE agent_id = :'agent_id'::bigint
  AND event_type = 'maintenance_attempt'
  AND event_at >= (extract(epoch FROM now() - interval '7 days') * 1000)::bigint
  AND properties->>'phase' = 'notification'
  AND properties->'notification'->>'action' = 'watch'
  AND properties->'notification'->'watch'->>'order_id' = :'order_id'
ORDER BY event_at, created_at, event_id
LIMIT 1000;
