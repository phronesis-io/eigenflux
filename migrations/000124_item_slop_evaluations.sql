-- +goose Up
SET LOCAL lock_timeout = '5s';
CREATE TABLE item_slop_evaluations (
 item_id BIGINT PRIMARY KEY REFERENCES raw_items(item_id) ON DELETE CASCADE,
 prompt_version TEXT NOT NULL,
 model TEXT NOT NULL,
 content_hash TEXT NOT NULL,
 template_hash TEXT NOT NULL,
 request_hash TEXT NOT NULL,
 response TEXT NOT NULL,
 is_slop SMALLINT CHECK (is_slop IN (0,1)),
 slop_reason TEXT NOT NULL DEFAULT '',
 slop_score DOUBLE PRECISION CHECK (slop_score BETWEEN 0 AND 1),
 model_quality DOUBLE PRECISION CHECK (model_quality BETWEEN 0 AND 1),
 effective_quality DOUBLE PRECISION CHECK (effective_quality BETWEEN 0 AND 1),
 discard BOOLEAN NOT NULL,
 blocked BOOLEAN NOT NULL,
 evaluated_at BIGINT NOT NULL,
 CHECK (discard OR (is_slop IS NOT NULL AND slop_score IS NOT NULL AND model_quality IS NOT NULL AND effective_quality IS NOT NULL AND length(trim(slop_reason)) > 0)),
 CHECK (blocked = (NOT discard AND COALESCE(is_slop = 1, false))),
 CHECK (NOT blocked OR effective_quality = 0)
);
CREATE INDEX item_slop_evaluations_time ON item_slop_evaluations(evaluated_at);
REVOKE ALL ON item_slop_evaluations FROM PUBLIC;

CREATE VIEW grafana_ugc_slop_hourly WITH (security_barrier=true) AS
SELECT date_trunc('hour', to_timestamp(evaluated_at / 1000.0)) AS hour,
 prompt_version, model, count(*) AS evaluated,
 count(*) FILTER (WHERE discard) AS discarded,
 count(*) FILTER (WHERE NOT discard) AS classified,
 count(*) FILTER (WHERE blocked) AS blocked,
 count(*) FILTER (WHERE blocked AND model_quality <> 0) AS model_quality_overrides,
 max(to_timestamp(evaluated_at / 1000.0)) AS latest_evaluation
FROM item_slop_evaluations
GROUP BY 1,2,3;
CREATE VIEW grafana_ugc_slop_scores WITH (security_barrier=true) AS
SELECT date_trunc('hour', to_timestamp(evaluated_at / 1000.0)) AS hour,
 prompt_version, model, least(floor(slop_score*10)::int,9) AS score_bucket,
 is_slop, count(*) AS items
FROM item_slop_evaluations WHERE NOT discard
GROUP BY 1,2,3,4,5;
REVOKE ALL ON grafana_ugc_slop_hourly, grafana_ugc_slop_scores FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='grafana_ro_v2') THEN
  GRANT SELECT ON grafana_ugc_slop_hourly, grafana_ugc_slop_scores TO grafana_ro_v2;
 END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP VIEW grafana_ugc_slop_hourly, grafana_ugc_slop_scores;
-- Auditing decisions survive code rollback. Destructive schema rollback is intentional.
DROP TABLE item_slop_evaluations;
