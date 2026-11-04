-- +goose Up
-- +goose StatementBegin
-- Reporters are Agents that report follow-ups; server-recorded detail_fetch rows
-- must not enlarge the delivery denominator.
CREATE OR REPLACE VIEW grafana_pgc_surface_7d
WITH (security_barrier = true) AS
WITH cut AS (
    SELECT (extract(epoch FROM now()) * 1000)::BIGINT - 604800000 AS since_ms
), reporters AS MATERIALIZED (
    SELECT DISTINCT f.agent_id
    FROM followup_labels f
    JOIN agents consumer ON consumer.agent_id = f.agent_id,
         cut
    WHERE f.reported_at >= cut.since_ms
      AND f.kind IN ('surface', 'question', 'discussion', 'task')
      AND lower(consumer.email) NOT LIKE '%bot.eigenflux%'
      AND lower(consumer.email) NOT LIKE '%pgc.eigenflux%'
), deliveries AS MATERIALIZED (
    SELECT
        r.agent_id,
        CASE
            WHEN lower(author.email) LIKE '%@pgc.eigenflux.one' THEN 'PGC'
            ELSE 'UGC'
        END AS lane
    FROM replay_logs r
    JOIN reporters q USING (agent_id)
    JOIN raw_items i USING (item_id)
    JOIN agents author ON author.agent_id = i.author_agent_id,
         cut
    WHERE r.delivered IS TRUE AND r.served_at >= cut.since_ms
), surfaces AS MATERIALIZED (
    SELECT
        f.agent_id,
        CASE
            WHEN lower(author.email) LIKE '%@pgc.eigenflux.one' THEN 'PGC'
            ELSE 'UGC'
        END AS lane
    FROM followup_labels f
    JOIN reporters q USING (agent_id)
    JOIN raw_items i USING (item_id)
    JOIN agents author ON author.agent_id = i.author_agent_id,
         cut
    WHERE f.kind = 'surface' AND f.reported_at >= cut.since_ms
), delivery_agg AS (
    SELECT lane, count(*) AS delivered, count(DISTINCT agent_id) AS reporters
    FROM deliveries
    GROUP BY lane
), surface_agg AS (
    SELECT lane, count(*) AS surfaced
    FROM surfaces
    GROUP BY lane
)
SELECT
    d.lane,
    d.reporters,
    d.delivered,
    COALESCE(s.surfaced, 0) AS surfaced,
    round(100.0 * COALESCE(s.surfaced, 0) / NULLIF(d.delivered, 0), 2) AS surface_rate
FROM delivery_agg d
LEFT JOIN surface_agg s USING (lane);
-- +goose StatementEnd

-- +goose Down
-- +goose StatementBegin
CREATE OR REPLACE VIEW grafana_pgc_surface_7d
WITH (security_barrier = true) AS
WITH cut AS (
    SELECT (extract(epoch FROM now()) * 1000)::BIGINT - 604800000 AS since_ms
), reporters AS MATERIALIZED (
    SELECT DISTINCT f.agent_id
    FROM followup_labels f
    JOIN agents consumer ON consumer.agent_id = f.agent_id,
         cut
    WHERE f.reported_at >= cut.since_ms
      AND lower(consumer.email) NOT LIKE '%bot.eigenflux%'
      AND lower(consumer.email) NOT LIKE '%pgc.eigenflux%'
), deliveries AS MATERIALIZED (
    SELECT
        r.agent_id,
        CASE
            WHEN lower(author.email) LIKE '%@pgc.eigenflux.one' THEN 'PGC'
            ELSE 'UGC'
        END AS lane
    FROM replay_logs r
    JOIN reporters q USING (agent_id)
    JOIN raw_items i USING (item_id)
    JOIN agents author ON author.agent_id = i.author_agent_id,
         cut
    WHERE r.delivered IS TRUE AND r.served_at >= cut.since_ms
), surfaces AS MATERIALIZED (
    SELECT
        f.agent_id,
        CASE
            WHEN lower(author.email) LIKE '%@pgc.eigenflux.one' THEN 'PGC'
            ELSE 'UGC'
        END AS lane
    FROM followup_labels f
    JOIN reporters q USING (agent_id)
    JOIN raw_items i USING (item_id)
    JOIN agents author ON author.agent_id = i.author_agent_id,
         cut
    WHERE f.kind = 'surface' AND f.reported_at >= cut.since_ms
), delivery_agg AS (
    SELECT lane, count(*) AS delivered, count(DISTINCT agent_id) AS reporters
    FROM deliveries
    GROUP BY lane
), surface_agg AS (
    SELECT lane, count(*) AS surfaced
    FROM surfaces
    GROUP BY lane
)
SELECT
    d.lane,
    d.reporters,
    d.delivered,
    COALESCE(s.surfaced, 0) AS surfaced,
    round(100.0 * COALESCE(s.surfaced, 0) / NULLIF(d.delivered, 0), 2) AS surface_rate
FROM delivery_agg d
LEFT JOIN surface_agg s USING (lane);
-- +goose StatementEnd
