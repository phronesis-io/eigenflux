-- +goose Up
SET LOCAL lock_timeout = '5s';

-- No fact-table scan or historical data mutation during deployment. The cron
-- fills one bounded Shanghai day atomically after migration.
CREATE TABLE recommendation_effect_daily (
    day DATE NOT NULL,
    basis TEXT NOT NULL CHECK (basis IN ('profile','need','baseline','friend','unknown','unattributed','search')),
    lane TEXT NOT NULL CHECK (lane IN ('pgc','ugc','official','unknown')),
    delivery_rows BIGINT NOT NULL,
    delivery_items BIGINT NOT NULL,
    delivery_agents BIGINT NOT NULL,
    unidentifiable_deliveries BIGINT NOT NULL,
    feedback_events BIGINT NOT NULL,
    feedback_agents BIGINT NOT NULL,
    score_neg1 BIGINT NOT NULL,
    score_0 BIGINT NOT NULL,
    score_1 BIGINT NOT NULL,
    score_2 BIGINT NOT NULL,
    surface_events BIGINT NOT NULL,
    question_events BIGINT NOT NULL,
    discussion_events BIGINT NOT NULL,
    task_events BIGINT NOT NULL,
    mature_exposures BIGINT NOT NULL,
    mature_scored_exposures BIGINT NOT NULL,
    mature_feedback_events BIGINT NOT NULL,
    mature_score_neg1 BIGINT NOT NULL,
    mature_score_0 BIGINT NOT NULL,
    mature_score_1 BIGINT NOT NULL,
    mature_score_2 BIGINT NOT NULL,
    snapshot_at TIMESTAMPTZ NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version = 1),
    PRIMARY KEY (day, basis, lane),
    CHECK (feedback_events = score_neg1 + score_0 + score_1 + score_2),
    CHECK (mature_feedback_events = mature_score_neg1 + mature_score_0 + mature_score_1 + mature_score_2),
    CHECK (mature_scored_exposures <= mature_exposures)
);
REVOKE ALL ON recommendation_effect_daily FROM PUBLIC;

CREATE VIEW grafana_recommendation_effect_daily WITH (security_barrier = true) AS
SELECT * FROM recommendation_effect_daily;
REVOKE ALL ON grafana_recommendation_effect_daily FROM PUBLIC;

-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro_v2') THEN
        GRANT SELECT ON grafana_recommendation_effect_daily TO grafana_ro_v2;
    END IF;
END $$;
-- +goose StatementEnd

COMMENT ON VIEW grafana_recommendation_effect_daily IS
    'Anonymous Shanghai-day broadcast recommendation aggregates. Exact impression/Agent/item attribution; other official content is separate. Rates require nonzero denominators. Refreshed in bounded daily batches by pipeline-cron.';

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP VIEW grafana_recommendation_effect_daily;
DROP TABLE recommendation_effect_daily;
