-- Read-only operator query. Supply canonical psql variables agent_id/order_id.
-- Authenticated Agent scope and retained event time are required for diagnosis.
SELECT to_timestamp(event_at/1000.0) AS observed_at,
       properties->>'attempt_id' AS receipt_id,
       properties->>'from_version' AS cli_version,
       properties->>'host' AS host,
       properties->>'result' AS result,
       properties->>'error_code' AS error_code,
       properties->>'duration_ms' AS duration_ms,
       properties->'commission' AS diagnostic
FROM telemetry_events_v2
WHERE agent_id=:'agent_id'::bigint
  AND event_type='maintenance_attempt'
  AND properties->>'component'='dispatch'
  AND properties->>'phase'='commission'
  AND properties->'commission'->>'order_id'=:'order_id'
  AND event_at >= (extract(epoch FROM now()-interval '30 days')*1000)::bigint
ORDER BY event_at,created_at,event_id
LIMIT 1000;
