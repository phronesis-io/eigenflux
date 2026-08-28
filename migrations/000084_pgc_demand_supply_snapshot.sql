-- +goose Up
SET LOCAL lock_timeout = '5s';
SET LOCAL statement_timeout = '5min';

-- The demand/supply view intentionally performs the privacy-sensitive joins
-- behind an aggregate boundary. That query took 11.9s and touched 11.2M
-- buffers when Grafana ran it directly. Preserve the original view as the
-- calculation contract and expose its six anonymous rows through a snapshot.
CREATE MATERIALIZED VIEW grafana_pgc_demand_supply_24h_snapshot AS
SELECT demand_supply.*, now() AS snapshot_at
FROM grafana_pgc_demand_supply_24h demand_supply;

CREATE UNIQUE INDEX uq_grafana_pgc_demand_supply_24h_snapshot_ord
    ON grafana_pgc_demand_supply_24h_snapshot (ord);

REVOKE ALL ON grafana_pgc_demand_supply_24h_snapshot FROM PUBLIC;

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro_v2') THEN
        GRANT SELECT ON grafana_pgc_demand_supply_24h_snapshot TO grafana_ro_v2;
    END IF;
END
$$;
-- +goose StatementEnd

COMMENT ON MATERIALIZED VIEW grafana_pgc_demand_supply_24h_snapshot IS
    'Anonymous 24-hour fixed-bucket demand and PGC exposure aggregates, '
    'refreshed every fifteen minutes for Grafana.';

-- +goose Down
SET LOCAL lock_timeout = '5s';

-- +goose StatementBegin
DO $$
BEGIN
    IF EXISTS (SELECT 1 FROM pg_roles WHERE rolname = 'grafana_ro_v2') THEN
        REVOKE SELECT ON grafana_pgc_demand_supply_24h_snapshot FROM grafana_ro_v2;
    END IF;
END
$$;
-- +goose StatementEnd

DROP MATERIALIZED VIEW IF EXISTS grafana_pgc_demand_supply_24h_snapshot;
