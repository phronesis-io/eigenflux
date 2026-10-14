-- Read-only observed samples. Registration/submission do not prove visibility.
-- Installation notification initialization, excluding manual and watch startup.
SELECT date_trunc('day', to_timestamp(event_at / 1000.0)) AS day,
       properties->>'platform' AS platform,
       properties->>'from_version' AS cli_version,
       properties->>'result' AS result,
       count(DISTINCT (agent_id, properties->>'attempt_id')) AS observed_attempts
FROM telemetry_events_v2
WHERE event_type = 'maintenance_attempt'
  AND properties->>'phase' = 'notification'
  AND properties->>'source' = 'installer'
  AND properties->'notification'->>'action' = 'setup'
GROUP BY 1, 2, 3, 4 ORDER BY 1 DESC, 2, 3, 4;

-- Latest outcome per business notification, excluding explicit tests.
WITH latest AS (
  SELECT DISTINCT ON (agent_id, properties->'notification'->>'id_hash')
         agent_id, event_at, properties
  FROM telemetry_events_v2
  WHERE event_type = 'maintenance_attempt'
    AND properties->>'phase' = 'notification'
    AND properties->>'source' = 'watch'
    AND properties->'notification'->>'action' = 'send'
  ORDER BY agent_id, properties->'notification'->>'id_hash',
           event_at DESC, created_at DESC, event_id DESC
)
SELECT properties->>'platform' AS platform,
       properties->>'from_version' AS cli_version,
       properties->>'result' AS latest_result,
       count(*) AS observed_notifications
FROM latest GROUP BY 1, 2, 3 ORDER BY 1, 2, 3;

-- Failure diagnostics contain only allowlisted codes and numeric HRESULTs.
SELECT properties->>'platform' AS platform,
       properties->>'error_code' AS code,
       properties->'notification'->>'stage' AS stage,
       properties->'notification'->>'hresult' AS hresult,
       count(*) AS observed_failures
FROM telemetry_events_v2
WHERE event_type = 'maintenance_attempt'
  AND properties->>'phase' = 'notification'
  AND properties->>'result' IN ('disabled', 'failed')
GROUP BY 1, 2, 3, 4 ORDER BY 5 DESC;
