-- +goose Up
SET LOCAL lock_timeout = '5s';
-- Additive schema only. Rows are filled by pipeline-cron in bounded daily transactions.
CREATE TABLE match_overview_daily (
    day DATE PRIMARY KEY,
    scored_pairs BIGINT NOT NULL CHECK (scored_pairs>=0),
    good_pairs BIGINT NOT NULL CHECK (good_pairs>=0 AND good_pairs<=scored_pairs),
    pgc_scored_pairs BIGINT NOT NULL CHECK (pgc_scored_pairs>=0),
    pgc_good_pairs BIGINT NOT NULL CHECK (pgc_good_pairs>=0 AND pgc_good_pairs<=pgc_scored_pairs),
    ugc_scored_pairs BIGINT NOT NULL CHECK (ugc_scored_pairs>=0),
    ugc_good_pairs BIGINT NOT NULL CHECK (ugc_good_pairs>=0 AND ugc_good_pairs<=ugc_scored_pairs),
    ugc_messaged_pairs BIGINT NOT NULL CHECK (ugc_messaged_pairs>=0 AND ugc_messaged_pairs<=ugc_good_pairs),
    ugc_mutual_pairs BIGINT NOT NULL CHECK (ugc_mutual_pairs>=0 AND ugc_mutual_pairs<=ugc_messaged_pairs),
    snapshot_at TIMESTAMPTZ NOT NULL,
    CHECK (pgc_scored_pairs+ugc_scored_pairs<=scored_pairs),
    CHECK (pgc_good_pairs+ugc_good_pairs<=good_pairs)
);
REVOKE ALL ON match_overview_daily FROM PUBLIC;
CREATE VIEW grafana_match_overview_daily WITH (security_barrier=true) AS
SELECT * FROM match_overview_daily;
REVOKE ALL ON grafana_match_overview_daily FROM PUBLIC;
-- +goose StatementBegin
DO $$ BEGIN
 IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname='grafana_ro_v2') THEN
  GRANT SELECT ON grafana_match_overview_daily TO grafana_ro_v2;
 END IF;
END $$;
-- +goose StatementEnd
COMMENT ON VIEW grafana_match_overview_daily IS
 'Per Shanghai day: distinct external (Agent, broadcast) pairs scored that day, those scored 1 or 2, split by PGC/UGC author; and UGC good pairs where the scorer messaged the author about that broadcast and the author replied within 72 hours. Counts only; no Agent or item identifiers.';
-- +goose Down
DROP VIEW grafana_match_overview_daily;
DROP TABLE match_overview_daily;
