-- +goose Up
SET LOCAL lock_timeout = '5s';

CREATE VIEW grafana_console_dau_daily
WITH (security_barrier = true) AS
SELECT
    (to_timestamp(usage.time_bucket / 1000.0) AT TIME ZONE 'Asia/Shanghai')::date AS activity_date,
    count(DISTINCT usage.agent_id) AS human_dau
FROM console_usage_sessions usage
JOIN agents agent ON agent.agent_id = usage.agent_id
WHERE usage.visible_duration_ms > 0
  AND agent.email NOT LIKE '%bot.eigenflux%'
  AND agent.email NOT LIKE '%pgc.eigenflux%'
  AND agent.email <> 'fmw19990718@gmail.com'
GROUP BY 1;

REVOKE ALL ON grafana_console_dau_daily FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro_v2') THEN
        GRANT SELECT ON grafana_console_dau_daily TO grafana_ro_v2;
    END IF;
END
$$;
-- +goose StatementEnd

COMMENT ON VIEW grafana_console_dau_daily IS
    'Privacy-bounded daily count of real users with visible Console usage, bucketed in Asia/Shanghai for the User Growth dashboard.';

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro_v2') THEN
        REVOKE SELECT ON grafana_console_dau_daily FROM grafana_ro_v2;
    END IF;
END
$$;
-- +goose StatementEnd

DROP VIEW IF EXISTS grafana_console_dau_daily;
