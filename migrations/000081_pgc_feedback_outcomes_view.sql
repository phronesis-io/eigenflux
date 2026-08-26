-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- PGC source governance needs repeated downstream-outcome evidence, but the
-- PGC host must never receive user-level feedback rows. Collapse each agent to
-- one vote distribution per source/source class first, then expose only groups
-- with a meaningful anonymity and evidence floor.
CREATE VIEW pgc_feedback_outcomes_7d
WITH (security_barrier = true) AS
WITH cut AS (
    SELECT (extract(epoch FROM now()) * 1000)::BIGINT - 604800000 AS since_ms
), feedback AS MATERIALIZED (
    SELECT
        f.agent_id,
        f.score,
        CASE
            WHEN i.raw_notes IS JSON THEN i.raw_notes::JSONB
            ELSE '{}'::JSONB
        END AS notes
    FROM feedback_logs f
    JOIN agents consumer ON consumer.agent_id = f.agent_id
    JOIN raw_items i USING (item_id)
    JOIN agents author ON author.agent_id = i.author_agent_id,
         cut
    WHERE f.feedback_at >= cut.since_ms
      AND lower(author.email) LIKE '%@pgc.eigenflux.one'
      AND lower(consumer.email) NOT LIKE '%bot.eigenflux%'
      AND lower(consumer.email) NOT LIKE '%pgc.eigenflux%'
), attributed AS MATERIALIZED (
    SELECT
        agent_id,
        score,
        nullif(trim(notes #>> '{evidence,source_name}'), '') AS source_name,
        COALESCE(
            nullif(trim(notes #>> '{evidence,source_class}'), ''),
            'unclassified'
        ) AS source_class
    FROM feedback
), scoped AS (
    SELECT agent_id, score, 'source'::TEXT AS scope, source_name AS name
    FROM attributed
    WHERE source_name IS NOT NULL
    UNION ALL
    SELECT agent_id, score, 'source_class'::TEXT AS scope, source_class AS name
    FROM attributed
), agent_bucket AS (
    SELECT
        scope,
        name,
        agent_id,
        count(*) AS events,
        count(*) FILTER (WHERE score IN (1, 2)) AS positive,
        count(*) FILTER (WHERE score = 2) AS strong_positive,
        count(*) FILTER (WHERE score = -1) AS discarded
    FROM scoped
    GROUP BY scope, name, agent_id
    HAVING count(*) >= 3
), aggregate_bucket AS (
    SELECT
        scope,
        name,
        count(*) AS agents,
        sum(events) AS feedback_events,
        round(avg(100.0 * positive / events), 1) AS agent_equal_positive_rate,
        round(avg(100.0 * discarded / events), 1) AS agent_equal_discard_rate,
        round(avg(100.0 * strong_positive / events), 1) AS agent_equal_strong_positive_rate,
        round(100.0 * sum(positive) / sum(events), 1) AS raw_positive_rate,
        round(100.0 * sum(discarded) / sum(events), 1) AS raw_discard_rate
    FROM agent_bucket
    GROUP BY scope, name
    HAVING count(*) >= 20 AND sum(events) >= 100
)
SELECT
    scope,
    name,
    agents,
    feedback_events,
    agent_equal_positive_rate,
    agent_equal_discard_rate,
    agent_equal_strong_positive_rate,
    raw_positive_rate,
    raw_discard_rate,
    round(100.0 * feedback_events / NULLIF((SELECT count(*) FROM feedback), 0), 2)
        AS feedback_event_share
FROM aggregate_bucket;

REVOKE ALL ON pgc_feedback_outcomes_7d FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgc_demand') THEN
        GRANT SELECT ON pgc_feedback_outcomes_7d TO pgc_demand;
        GRANT SELECT ON grafana_pgc_feedback_7d TO pgc_demand;
        GRANT SELECT ON grafana_pgc_surface_7d TO pgc_demand;
    END IF;
END
$$;
-- +goose StatementEnd

COMMENT ON VIEW pgc_feedback_outcomes_7d IS
    'Anonymous seven-day PGC source outcomes. Each agent is equal-weighted; '
    'only groups with at least 20 agents and 100 feedback events are exposed.';

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'pgc_demand') THEN
        REVOKE SELECT ON pgc_feedback_outcomes_7d FROM pgc_demand;
        REVOKE SELECT ON grafana_pgc_feedback_7d FROM pgc_demand;
        REVOKE SELECT ON grafana_pgc_surface_7d FROM pgc_demand;
    END IF;
END
$$;
-- +goose StatementEnd

DROP VIEW IF EXISTS pgc_feedback_outcomes_7d;
