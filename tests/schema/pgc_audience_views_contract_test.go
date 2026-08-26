package schema_test

import (
	"os"
	"strings"
	"testing"
)

func TestPGCAudienceViewsKeepGrafanaOnAnonymousAggregates(t *testing.T) {
	const migration = "../../migrations/000075_pgc_audience_aggregate_views.sql"
	contents, err := os.ReadFile(migration)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(contents)

	views := []string{
		"grafana_pgc_audience_reach_24h",
		"grafana_pgc_demand_supply_24h",
		"grafana_pgc_feedback_7d",
		"grafana_pgc_surface_7d",
		"grafana_pgc_profile_completeness",
	}
	for _, view := range views {
		if !strings.Contains(sql, "CREATE VIEW "+view+"\nWITH (security_barrier = true)") {
			t.Errorf("%s must be a security-barrier view", view)
		}
		if !strings.Contains(sql, "REVOKE ALL ON "+view+" FROM PUBLIC") {
			t.Errorf("%s must revoke PUBLIC access", view)
		}
		if !strings.Contains(sql, "GRANT SELECT ON "+view+" TO grafana_ro_v2") {
			t.Errorf("%s must grant only aggregate access to Grafana", view)
		}
	}

	for _, forbidden := range []string{
		"GRANT SELECT ON replay_logs",
		"GRANT SELECT ON feedback_logs",
		"GRANT SELECT ON followup_labels",
		"GRANT SELECT ON agent_profiles",
		"GRANT SELECT ON raw_items",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration must not grant Grafana user-level data: %s", forbidden)
		}
	}
}
