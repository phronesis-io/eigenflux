package schema_test

import (
	"os"
	"strings"
	"testing"
)

func TestPGCStrongFeedbackTrendKeepsEventRowsPrivate(t *testing.T) {
	const migration = "../../migrations/000082_pgc_strong_feedback_trend.sql"
	contents, err := os.ReadFile(migration)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(contents)

	for _, required := range []string{
		"CREATE VIEW grafana_pgc_strong_feedback_daily\nWITH (security_barrier = true)",
		"AT TIME ZONE 'Asia/Shanghai'",
		"count(*) FILTER (WHERE score = 2) AS strong_positive_events",
		"count(DISTINCT agent_id) AS feedback_agents",
		"REVOKE ALL ON grafana_pgc_strong_feedback_daily FROM PUBLIC",
		"GRANT SELECT ON grafana_pgc_strong_feedback_daily TO grafana_ro_v2",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing privacy/trend contract: %s", required)
		}
	}

	for _, forbidden := range []string{
		"GRANT SELECT ON feedback_logs",
		"GRANT SELECT ON raw_items",
		"GRANT SELECT ON agents",
		"agent_id AS",
		"item_id AS",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration exposes user-level data: %s", forbidden)
		}
	}
}
