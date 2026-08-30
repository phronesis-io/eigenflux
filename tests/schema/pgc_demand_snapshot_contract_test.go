package schema_test

import (
	"os"
	"strings"
	"testing"
)

func TestPGCDemandSnapshotMigrationContract(t *testing.T) {
	b, err := os.ReadFile("../../migrations/000084_pgc_demand_supply_snapshot.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, want := range []string{
		"CREATE MATERIALIZED VIEW grafana_pgc_demand_supply_24h_snapshot",
		"FROM grafana_pgc_demand_supply_24h demand_supply",
		"CREATE UNIQUE INDEX uq_grafana_pgc_demand_supply_24h_snapshot_ord",
		"snapshot_at",
		"REVOKE ALL ON grafana_pgc_demand_supply_24h_snapshot FROM PUBLIC",
		"GRANT SELECT ON grafana_pgc_demand_supply_24h_snapshot TO grafana_ro_v2",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
		}
	}
	for _, forbidden := range []string{
		"GRANT SELECT ON replay_logs",
		"GRANT SELECT ON agent_profiles",
		"GRANT SELECT ON raw_items",
		"agent_id AS",
		"item_id AS",
	} {
		if strings.Contains(sql, forbidden) {
			t.Fatalf("migration exposes user-level data: %s", forbidden)
		}
	}
}

func TestPGCDemandSnapshotRuntimeContract(t *testing.T) {
	b, err := os.ReadFile("../../pipeline/cron/pgc_demand_snapshot.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		"REFRESH MATERIALIZED VIEW CONCURRENTLY grafana_pgc_demand_supply_24h_snapshot",
		"pgcDemandRefreshInterval = 15 * time.Minute",
		"refreshPGCDemandSnapshotWithLock",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("runtime missing %q", want)
		}
	}
}
