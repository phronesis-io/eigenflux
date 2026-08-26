package schema_test

import (
	"os"
	"strings"
	"testing"
)

func TestPGCFeedbackOutcomesViewKeepsUserRowsPrivate(t *testing.T) {
	const migration = "../../migrations/000081_pgc_feedback_outcomes_view.sql"
	contents, err := os.ReadFile(migration)
	if err != nil {
		t.Fatalf("read migration: %v", err)
	}
	sql := string(contents)

	for _, required := range []string{
		"CREATE VIEW pgc_feedback_outcomes_7d\nWITH (security_barrier = true)",
		"GROUP BY scope, name, agent_id",
		"HAVING count(*) >= 3",
		"HAVING count(*) >= 20 AND sum(events) >= 100",
		"REVOKE ALL ON pgc_feedback_outcomes_7d FROM PUBLIC",
		"GRANT SELECT ON pgc_feedback_outcomes_7d TO pgc_demand",
	} {
		if !strings.Contains(sql, required) {
			t.Errorf("missing privacy/outcome contract: %s", required)
		}
	}

	for _, forbidden := range []string{
		"GRANT SELECT ON feedback_logs TO pgc_demand",
		"GRANT SELECT ON raw_items TO pgc_demand",
		"GRANT SELECT ON agents TO pgc_demand",
		"GRANT SELECT ON agent_profiles TO pgc_demand",
	} {
		if strings.Contains(sql, forbidden) {
			t.Errorf("migration exposes user-level data: %s", forbidden)
		}
	}
}
