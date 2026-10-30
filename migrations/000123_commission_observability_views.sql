-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- Client diagnostics are optional evidence; older CLI versions under-report.
-- Restrict every dimension before exposing anonymous daily aggregates.
CREATE VIEW grafana_commission_dispatch_daily WITH (security_barrier=true) AS
SELECT (to_timestamp(event_at / 1000.0) AT TIME ZONE 'Asia/Shanghai')::date AS day,
       properties->'commission'->>'job_kind' AS job_kind,
       properties->'commission'->>'stage' AS stage,
       properties->>'result' AS result,
       CASE WHEN properties->'commission'->>'adapter' IN ('acp','codex_turn','workbuddy_bridge','command','native')
            THEN properties->'commission'->>'adapter' ELSE 'unknown' END AS adapter,
       count(*) AS event_count,
       count(*) FILTER (WHERE (properties->>'duration_ms') ~ '^[0-9]{1,8}$') AS duration_count,
       sum(CASE WHEN (properties->>'duration_ms') ~ '^[0-9]{1,8}$'
                THEN least((properties->>'duration_ms')::bigint,86400000) ELSE 0 END) AS duration_ms_sum
FROM telemetry_events_v2
WHERE event_type='maintenance_attempt'
  AND properties->>'component'='dispatch' AND properties->>'phase'='commission'
  AND event_at >= (extract(epoch FROM now() - interval '30 days') * 1000)::bigint
  AND properties->'commission'->>'job_kind' IN ('commission_order','commission_fulfillment','commission_clarification','main_notice','material_notice')
  AND properties->'commission'->>'stage' IN ('queued','claimed','preflight','agent','artifact','upload_grant','upload_put','upload_confirm','deliver','verify','main_notice','input_check','accept','job_finished','retry')
  AND properties->>'result' IN ('queued','started','succeeded','failed','blocked','retry_scheduled','skipped')
GROUP BY 1,2,3,4,5;

-- ACK means durable local receipt, not Agent completion or human visibility.
CREATE VIEW grafana_commission_notification_delivery WITH (security_barrier=true) AS
WITH observed AS (
    SELECT CASE WHEN event_kind IN ('order.state.changed.v1','order.inputs.requested.v1','order.inputs.supplied.v1',
                                    'order.review.created.v1','order.refund.processing_error.v1','order.refund.completed.v1',
                                    'order.settlement.processing_error.v1','order.settlement.completed.v1','order.validation.processing_error.v1')
                THEN event_kind ELSE 'other' END AS event_kind,
           received_at, expires_at, acknowledged_at,
           (extract(epoch FROM now()) * 1000)::bigint AS now_ms
    FROM notification_inbox WHERE source_type='commission_order'
)
SELECT event_kind,
       count(*) FILTER (WHERE acknowledged_at IS NULL AND expires_at > now_ms) AS pending_count,
       coalesce(max(greatest(0,now_ms-received_at)/1000.0) FILTER (WHERE acknowledged_at IS NULL AND expires_at > now_ms),0) AS oldest_pending_age_seconds,
       count(*) FILTER (WHERE acknowledged_at >= now_ms-2592000000) AS acked_count,
       coalesce(sum(greatest(0,acknowledged_at-received_at)) FILTER (WHERE acknowledged_at >= now_ms-2592000000),0) AS ack_latency_ms_sum,
       count(*) FILTER (WHERE acknowledged_at >= now_ms-2592000000) AS ack_latency_count
FROM observed
GROUP BY event_kind;

REVOKE ALL ON grafana_commission_dispatch_daily, grafana_commission_notification_delivery FROM PUBLIC;
-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro_v2') THEN
        GRANT SELECT ON grafana_commission_dispatch_daily, grafana_commission_notification_delivery TO grafana_ro_v2;
    END IF;
END
$$;
-- +goose StatementEnd
COMMENT ON VIEW grafana_commission_dispatch_daily IS 'Anonymous 30-day client diagnostic aggregates; deployment and client adoption are separate. Duration is optional and started events do not establish completion.';
COMMENT ON VIEW grafana_commission_notification_delivery IS 'Current unexpired pending receipts and trailing-30-day ACK latency from inbox receipt. ACK is local durable intake, not execution success.';

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP VIEW IF EXISTS grafana_commission_dispatch_daily;
DROP VIEW IF EXISTS grafana_commission_notification_delivery;
