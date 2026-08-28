-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- The daily strong-feedback panel used to execute the full 90-day join on
-- every Grafana refresh. Production evidence on 2026-08-28 showed one read
-- touching 7.2M buffers and taking 4.86s. Keep the anonymous aggregate, but
-- compute it once per refresh instead of once per viewer.
DROP VIEW IF EXISTS grafana_pgc_strong_feedback_daily;

CREATE MATERIALIZED VIEW grafana_pgc_strong_feedback_daily AS
WITH bounds AS (
    SELECT
        (now() AT TIME ZONE 'Asia/Shanghai')::date AS today,
        ((extract(epoch FROM now()) * 1000)::BIGINT - 7862400000) AS since_ms,
        now() AS snapshot_at
), calendar AS (
    SELECT generate_series(today - 89, today, interval '1 day')::date AS day
    FROM bounds
), feedback AS MATERIALIZED (
    SELECT
        (to_timestamp(f.feedback_at / 1000.0)
            AT TIME ZONE 'Asia/Shanghai')::date AS day,
        f.agent_id,
        f.score
    FROM feedback_logs f
    JOIN agents consumer ON consumer.agent_id = f.agent_id
    JOIN raw_items i USING (item_id)
    JOIN agents author ON author.agent_id = i.author_agent_id,
         bounds
    WHERE f.feedback_at >= bounds.since_ms
      AND lower(author.email) LIKE '%@pgc.eigenflux.one'
      AND lower(consumer.email) NOT LIKE '%bot.eigenflux%'
      AND lower(consumer.email) NOT LIKE '%pgc.eigenflux%'
), daily AS (
    SELECT
        day,
        count(*) AS feedback_events,
        count(*) FILTER (WHERE score = 2) AS strong_positive_events,
        count(DISTINCT agent_id) AS feedback_agents
    FROM feedback
    GROUP BY day
)
SELECT
    c.day,
    COALESCE(d.feedback_events, 0) AS feedback_events,
    COALESCE(d.strong_positive_events, 0) AS strong_positive_events,
    COALESCE(d.feedback_agents, 0) AS feedback_agents,
    round(
        100.0 * COALESCE(d.strong_positive_events, 0)
            / NULLIF(d.feedback_events, 0),
        1
    ) AS strong_positive_rate,
    b.snapshot_at
FROM calendar c
LEFT JOIN daily d USING (day)
CROSS JOIN bounds b
ORDER BY c.day;

CREATE UNIQUE INDEX uq_grafana_pgc_strong_feedback_daily_day
    ON grafana_pgc_strong_feedback_daily (day);

REVOKE ALL ON grafana_pgc_strong_feedback_daily FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro_v2') THEN
        GRANT SELECT ON grafana_pgc_strong_feedback_daily TO grafana_ro_v2;
    END IF;
END
$$;
-- +goose StatementEnd
COMMENT ON MATERIALIZED VIEW grafana_pgc_strong_feedback_daily IS
    'Anonymous daily PGC score=2 feedback totals, refreshed every five minutes. '
    'Days use Asia/Shanghai event time; user-level feedback remains private.';

-- +goose Down
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro_v2') THEN
        REVOKE SELECT ON grafana_pgc_strong_feedback_daily FROM grafana_ro_v2;
    END IF;
END
$$;
-- +goose StatementEnd

DROP MATERIALIZED VIEW IF EXISTS grafana_pgc_strong_feedback_daily;

CREATE VIEW grafana_pgc_strong_feedback_daily
WITH (security_barrier = true) AS
WITH bounds AS (
    SELECT
        (now() AT TIME ZONE 'Asia/Shanghai')::date AS today,
        ((extract(epoch FROM now()) * 1000)::BIGINT - 7862400000) AS since_ms
), calendar AS (
    SELECT generate_series(today - 89, today, interval '1 day')::date AS day
    FROM bounds
), feedback AS MATERIALIZED (
    SELECT
        (to_timestamp(f.feedback_at / 1000.0)
            AT TIME ZONE 'Asia/Shanghai')::date AS day,
        f.agent_id,
        f.score
    FROM feedback_logs f
    JOIN agents consumer ON consumer.agent_id = f.agent_id
    JOIN raw_items i USING (item_id)
    JOIN agents author ON author.agent_id = i.author_agent_id,
         bounds
    WHERE f.feedback_at >= bounds.since_ms
      AND lower(author.email) LIKE '%@pgc.eigenflux.one'
      AND lower(consumer.email) NOT LIKE '%bot.eigenflux%'
      AND lower(consumer.email) NOT LIKE '%pgc.eigenflux%'
), daily AS (
    SELECT
        day,
        count(*) AS feedback_events,
        count(*) FILTER (WHERE score = 2) AS strong_positive_events,
        count(DISTINCT agent_id) AS feedback_agents
    FROM feedback
    GROUP BY day
)
SELECT
    c.day,
    COALESCE(d.feedback_events, 0) AS feedback_events,
    COALESCE(d.strong_positive_events, 0) AS strong_positive_events,
    COALESCE(d.feedback_agents, 0) AS feedback_agents,
    round(
        100.0 * COALESCE(d.strong_positive_events, 0)
            / NULLIF(d.feedback_events, 0),
        1
    ) AS strong_positive_rate
FROM calendar c
LEFT JOIN daily d USING (day)
ORDER BY c.day;

REVOKE ALL ON grafana_pgc_strong_feedback_daily FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro_v2') THEN
        GRANT SELECT ON grafana_pgc_strong_feedback_daily TO grafana_ro_v2;
    END IF;
END
$$;
-- +goose StatementEnd
