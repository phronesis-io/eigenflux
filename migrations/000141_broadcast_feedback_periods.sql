-- +goose Up
SET LOCAL lock_timeout = '5s';
CREATE TABLE broadcast_feedback_periods (
    version bigint GENERATED ALWAYS AS IDENTITY PRIMARY KEY,
    period_start date NOT NULL,
    published_at timestamptz NOT NULL DEFAULT clock_timestamp(),
    inputs timestamptz[] NOT NULL,
    exposures bigint NOT NULL,
    scored bigint NOT NULL,
    batches bigint NOT NULL,
    fully_scored bigint NOT NULL,
    unscored bigint NOT NULL,
    reason text NOT NULL
);
CREATE INDEX broadcast_feedback_period_latest ON broadcast_feedback_periods(period_start, version DESC);

-- +goose StatementBegin
CREATE FUNCTION publish_broadcast_feedback(lo date, hi date, cause text) RETURNS void
LANGUAGE sql AS $$
    INSERT INTO broadcast_feedback_periods(period_start, inputs, exposures, scored, batches, fully_scored, unscored, reason)
    SELECT start_day::date, array_agg(s.snapshot_at ORDER BY s.day),
           sum(mature_exposures), sum(scored_48h_exposures), sum(mature_batches),
           sum(fully_scored_batches), sum(unscored_batches), cause
    FROM generate_series(lo::timestamp, hi::timestamp, interval '1 day') start_day
    JOIN feedback_compliance_daily s ON s.day BETWEEN start_day::date-2 AND start_day::date+4
    WHERE s.dimension='all' AND s.value='all'
      AND s.snapshot_at >= (s.day+3)::timestamp AT TIME ZONE 'Asia/Shanghai'
    GROUP BY start_day
    HAVING count(*)=7 AND NOT EXISTS (
        SELECT FROM (SELECT * FROM broadcast_feedback_periods p WHERE p.period_start=start_day::date
                     ORDER BY version DESC LIMIT 1) old
        WHERE old.inputs=array_agg(s.snapshot_at ORDER BY s.day)
          AND (old.exposures,old.scored,old.batches,old.fully_scored,old.unscored)=
              (sum(mature_exposures),sum(scored_48h_exposures),sum(mature_batches),sum(fully_scored_batches),sum(unscored_batches))
    )
$$;
-- +goose StatementEnd

-- +goose StatementBegin
CREATE FUNCTION publish_final_feedback_day() RETURNS trigger LANGUAGE plpgsql AS $$
BEGIN
    -- Adjacent delivery days may complete on separate connections but share periods.
    PERFORM pg_advisory_xact_lock(141, 1);
    PERFORM publish_broadcast_feedback(NEW.day-4, NEW.day+2, 'final delivery day ' || NEW.day);
    RETURN NULL;
END
$$;
-- +goose StatementEnd
CREATE TRIGGER publish_final_feedback AFTER INSERT OR UPDATE ON feedback_compliance_daily
FOR EACH ROW WHEN (NEW.dimension='all' AND NEW.value='all' AND
    NEW.snapshot_at >= (NEW.day+3)::timestamp AT TIME ZONE 'Asia/Shanghai')
EXECUTE FUNCTION publish_final_feedback_day();

SELECT publish_broadcast_feedback(min(day)+2, max(day)-4, 'initial publication')
FROM feedback_compliance_daily WHERE dimension='all' AND value='all';
CREATE VIEW grafana_broadcast_feedback_periods WITH (security_barrier=true) AS
SELECT DISTINCT ON (period_start) period_start, version, published_at, exposures, scored, batches, fully_scored, unscored
FROM broadcast_feedback_periods ORDER BY period_start, version DESC;
REVOKE ALL ON grafana_broadcast_feedback_periods FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT FROM pg_roles WHERE rolname='grafana_ro_v2') THEN
   GRANT SELECT ON grafana_broadcast_feedback_periods TO grafana_ro_v2;
 END IF;
END $$;
-- +goose StatementEnd

-- +goose Down
DROP TRIGGER publish_final_feedback ON feedback_compliance_daily;
DROP FUNCTION publish_final_feedback_day();
DROP VIEW grafana_broadcast_feedback_periods;
DROP FUNCTION publish_broadcast_feedback(date,date,text);
DROP TABLE broadcast_feedback_periods;
