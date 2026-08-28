package schema_test

import (
	"os"
	"strings"
	"testing"
)

func TestPGCStrongFeedbackSnapshotMigrationContract(t *testing.T) {
	b, err := os.ReadFile("../../migrations/000083_pgc_strong_feedback_snapshot.sql")
	if err != nil {
		t.Fatal(err)
	}
	sql := string(b)
	for _, want := range []string{
		"CREATE MATERIALIZED VIEW grafana_pgc_strong_feedback_daily",
		"CREATE UNIQUE INDEX uq_grafana_pgc_strong_feedback_daily_day",
		"snapshot_at",
		"REVOKE ALL ON grafana_pgc_strong_feedback_daily FROM PUBLIC",
		"GRANT SELECT ON grafana_pgc_strong_feedback_daily TO grafana_ro_v2",
	} {
		if !strings.Contains(sql, want) {
			t.Fatalf("migration missing %q", want)
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
			t.Fatalf("migration exposes user-level data: %s", forbidden)
		}
	}
}

func TestPGCStrongFeedbackSnapshotRuntimeContract(t *testing.T) {
	b, err := os.ReadFile("../../pipeline/cron/pgc_feedback_snapshot.go")
	if err != nil {
		t.Fatal(err)
	}
	src := string(b)
	for _, want := range []string{
		"REFRESH MATERIALIZED VIEW CONCURRENTLY grafana_pgc_strong_feedback_daily",
		"pgcFeedbackRefreshInterval = 5 * time.Minute",
		"refreshPGCFeedbackSnapshotWithLock",
	} {
		if !strings.Contains(src, want) {
			t.Fatalf("runtime missing %q", want)
		}
	}
}
