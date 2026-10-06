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

-- Hourly activity is derived from the same exact-attribution snapshot as daily
-- activity. Future hours have no rows; observed empty hours have explicit zeroes.
CREATE TABLE recommendation_effect_hourly (
    hour_start TIMESTAMPTZ NOT NULL CHECK (mod(extract(epoch FROM hour_start),3600)=0),
    basis TEXT NOT NULL CHECK (basis IN ('profile','need','baseline','friend','unknown','unattributed','search')),
    lane TEXT NOT NULL CHECK (lane IN ('pgc','ugc','official','unknown')),
    delivery_rows BIGINT NOT NULL CHECK (delivery_rows>=0),
    feedback_events BIGINT NOT NULL CHECK (feedback_events>=0),
    score_neg1 BIGINT NOT NULL CHECK (score_neg1>=0),
    score_0 BIGINT NOT NULL CHECK (score_0>=0),
    score_1 BIGINT NOT NULL CHECK (score_1>=0),
    score_2 BIGINT NOT NULL CHECK (score_2>=0),
    snapshot_at TIMESTAMPTZ NOT NULL,
    schema_version INTEGER NOT NULL DEFAULT 1 CHECK (schema_version=1),
    PRIMARY KEY (hour_start,basis,lane),
    CHECK (feedback_events=score_neg1+score_0+score_1+score_2)
);
REVOKE ALL ON recommendation_effect_hourly FROM PUBLIC;
CREATE VIEW grafana_recommendation_effect_hourly WITH (security_barrier=true) AS
SELECT * FROM recommendation_effect_hourly;
REVOKE ALL ON grafana_recommendation_effect_hourly FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='grafana_ro_v2') THEN
        GRANT SELECT ON grafana_recommendation_effect_hourly TO grafana_ro_v2;
    END IF;
END $$;
-- +goose StatementEnd
COMMENT ON VIEW grafana_recommendation_effect_hourly IS
    'Anonymous hourly broadcast delivery and score-event activity. Exact attribution shared with daily observations; current hour is partial. Zero-score rates remain unknown.';

-- +goose Down
SET LOCAL lock_timeout = '5s';
DROP VIEW grafana_recommendation_effect_hourly;
DROP TABLE recommendation_effect_hourly;
DROP VIEW grafana_recommendation_effect_daily;
DROP TABLE recommendation_effect_daily;
