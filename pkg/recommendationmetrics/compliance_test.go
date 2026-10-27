package recommendationmetrics

import (
	"context"
	"database/sql"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"
)

const complianceDay = "2026-10-02"

var complianceServed = instant("2026-10-02T12:00:00+08:00")
var complianceCutoff = instant("2026-10-05T00:00:00+08:00")

func complianceDB(t *testing.T) *sql.DB {
	t.Helper()
	c := fixtureDB(t)
	for _, name := range []string{"000118_feedback_compliance_daily.sql", "000119_feedback_compliance_inferred.sql"} {
		b, err := os.ReadFile("../../migrations/" + name)
		if err != nil {
			t.Fatal(err)
		}
		up, _, _ := strings.Cut(string(b), "-- +goose Down")
		complianceExec(t, c, up)
	}
	complianceExec(t, c, `CREATE INDEX idx_replay_logs_item ON replay_logs(item_id,served_at)`)
	return c
}

func complianceExec(t *testing.T, c *sql.DB, query string, args ...any) {
	t.Helper()
	if _, err := c.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func complianceDelivery(t *testing.T, c *sql.DB, id, agent, item int, imp string, served time.Time) {
	t.Helper()
	complianceExec(t, c, `INSERT INTO replay_logs VALUES($1,$2,$3,$4,$5,true,'broadcast','need_search_v1',2,'recommendation',0,
 '{"search":{"context":{"input_origin":"agent_context"}}}')`, id, agent, item, imp, served.UnixMilli())
}

func complianceScore(t *testing.T, c *sql.DB, agent, item int, imp string, after time.Duration, score int) {
	t.Helper()
	complianceExec(t, c, `INSERT INTO feedback_logs(agent_id,item_id,impression_id,feedback_at,score) VALUES($1,$2,$3,$4,$5)`, agent, item, imp, complianceServed.Add(after).UnixMilli(), score)
}

func complianceFollowup(t *testing.T, c *sql.DB, agent, item int, imp, kind string, after time.Duration) {
	t.Helper()
	complianceExec(t, c, `INSERT INTO followup_labels VALUES($1,$2,$3,$4,$5)`, agent, item, imp, complianceServed.Add(after).UnixMilli(), kind)
}

func refreshCompliance(t *testing.T, c *sql.DB) {
	t.Helper()
	if err := RefreshComplianceDay(context.Background(), c, complianceDay, complianceCutoff); err != nil {
		t.Fatal(err)
	}
}

func complianceCounts(t *testing.T, c *sql.DB, dimension, value string) map[string]int64 {
	t.Helper()
	rows, err := c.Query(`SELECT j.key,(j.value::text)::bigint FROM feedback_compliance_daily d,
 jsonb_each(to_jsonb(d)-ARRAY['day','dimension','value','snapshot_at','schema_version']) j
 WHERE day=$1 AND dimension=$2 AND d.value=$3`, complianceDay, dimension, value)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	got := map[string]int64{}
	for rows.Next() {
		var k string
		var n int64
		if err := rows.Scan(&k, &n); err != nil {
			t.Fatal(err)
		}
		got[k] = n
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(got) == 0 {
		t.Fatalf("missing cohort %s/%s", dimension, value)
	}
	return got
}

func assertComplianceCounts(t *testing.T, c *sql.DB, want map[string]int64) {
	t.Helper()
	got := complianceCounts(t, c, "all", "all")
	for k, n := range got {
		if n != want[k] {
			t.Errorf("%s=%d want %d", k, n, want[k])
		}
	}
	for k := range want {
		if _, ok := got[k]; !ok {
			t.Errorf("missing column %s", k)
		}
	}
}

func assertComplianceConservation(t *testing.T, c *sql.DB) {
	t.Helper()
	var mismatches int
	err := c.QueryRow(`WITH counts AS (
 SELECT dimension,key,(j.value::text)::bigint AS n FROM feedback_compliance_daily d,
 jsonb_each(to_jsonb(d)-ARRAY['day','dimension','value','snapshot_at','schema_version','delivered_agents']) j
 WHERE day=$1)
 SELECT count(*) FROM (SELECT dimension,key,sum(n) n FROM counts WHERE dimension<>'all' GROUP BY dimension,key) s
 JOIN counts a ON a.dimension='all' AND a.key=s.key WHERE a.n<>s.n`, complianceDay).Scan(&mismatches)
	if err != nil || mismatches != 0 {
		t.Fatalf("conservation mismatches=%d err=%v", mismatches, err)
	}
}

func assertComplianceReconciliation(t *testing.T, c *sql.DB, day string, cutoff time.Time) {
	t.Helper()
	if err := RefreshComplianceDay(context.Background(), c, day, cutoff); err != nil {
		t.Fatal(err)
	}
	if err := RefreshDay(context.Background(), c, day, cutoff); err != nil {
		t.Fatal(err)
	}
	for _, pair := range [][2]string{
		{"delivered_exposures", "sum(delivery_rows)"},
		{"identifiable_exposures", "sum(delivery_rows-unidentifiable_deliveries)"},
		{"mature_exposures", "sum(mature_exposures)"},
		{"scored_48h_exposures", "sum(mature_scored_exposures)"},
		{"feedback_events", "sum(feedback_events)"},
		{"feedback_events_exact", "sum(feedback_events) FILTER (WHERE basis<>'unattributed')"},
		{"surface_events", "sum(surface_events)"},
	} {
		var got, want int64
		err := c.QueryRow(`SELECT c.`+pair[0]+`,r.n FROM feedback_compliance_daily c CROSS JOIN
 (SELECT `+pair[1]+` AS n FROM recommendation_effect_daily WHERE day=$1) r
 WHERE c.day=$1 AND c.dimension='all' AND c.value='all'`, day).Scan(&got, &want)
		if err != nil || got != want {
			t.Errorf("reconcile %s=%d want %d err=%v", pair[0], got, want, err)
		}
	}
}

func TestPostgresComplianceCoverageWindows(t *testing.T) {
	c := complianceDB(t)
	for i, after := range []time.Duration{30 * time.Minute, 5 * time.Hour, 30 * time.Hour, 50 * time.Hour} {
		complianceDelivery(t, c, i+1, 1, 10+i, "coverage", complianceServed)
		complianceScore(t, c, 1, 10+i, "coverage", after, 1)
	}
	refreshCompliance(t, c)
	assertComplianceCounts(t, c, map[string]int64{
		"delivered_exposures": 4, "delivered_agents": 1, "identifiable_exposures": 4, "mature_exposures": 4,
		"scored_1h_exposures": 1, "scored_24h_exposures": 2, "scored_48h_exposures": 3,
		"mature_batches": 1, "partially_scored_batches": 1, "positive_exposures": 3,
		"positive_batches": 1, "positive_batches_without_surface": 1,
		"feedback_events": 2, "feedback_events_with_impression": 2, "feedback_events_exact": 2,
	})
	assertComplianceReconciliation(t, c, complianceDay, complianceCutoff)
	assertComplianceConservation(t, c)
}

func TestPostgresComplianceBatchSplitAndFinalScores(t *testing.T) {
	c := complianceDB(t)
	id := 0
	for _, batch := range []struct {
		imp       string
		n, scored int
	}{{"full", 5, 5}, {"partial", 5, 4}, {"none", 2, 0}} {
		for i := 0; i < batch.n; i++ {
			id++
			complianceDelivery(t, c, id, 1, id, batch.imp, complianceServed)
			if i < batch.scored {
				complianceScore(t, c, 1, id, batch.imp, time.Minute, 0)
				// Same timestamp: the larger event id determines the final score.
				complianceScore(t, c, 1, id, batch.imp, time.Minute, 1)
				complianceScore(t, c, 1, id, batch.imp, 48*time.Hour, 2)
				complianceScore(t, c, 1, id, batch.imp, 2*time.Minute, 99)
			}
		}
	}
	refreshCompliance(t, c)
	assertComplianceCounts(t, c, map[string]int64{
		"delivered_exposures": 12, "delivered_agents": 1, "identifiable_exposures": 12, "mature_exposures": 12,
		"scored_1h_exposures": 9, "scored_24h_exposures": 9, "scored_48h_exposures": 9,
		"mature_batches": 3, "fully_scored_batches": 1, "partially_scored_batches": 1, "unscored_batches": 1,
		"degenerate_candidate_batches": 1, "degenerate_batches": 1, "positive_exposures": 9,
		"positive_batches": 2, "positive_batches_without_surface": 2,
		"feedback_events": 18, "feedback_events_with_impression": 18, "feedback_events_exact": 18,
	})
	// A fifth scored value with a different final score makes this candidate nondegenerate.
	complianceScore(t, c, 1, 5, "full", 3*time.Minute, -1)
	refreshCompliance(t, c)
	got := complianceCounts(t, c, "all", "all")
	if got["degenerate_batches"] != 0 || got["degenerate_candidate_batches"] != 1 {
		t.Fatal(got)
	}
	assertComplianceConservation(t, c)
}

func TestPostgresComplianceSurface(t *testing.T) {
	c := complianceDB(t)
	for i := 1; i <= 5; i++ {
		complianceDelivery(t, c, i, 1, i, fmt.Sprint(i), complianceServed)
	}
	complianceScore(t, c, 1, 1, "1", time.Minute, 1)
	complianceFollowup(t, c, 1, 2, "2", "surface", time.Minute)
	complianceScore(t, c, 1, 3, "3", time.Minute, 1)
	complianceScore(t, c, 1, 3, "3", 2*time.Minute, 0)
	complianceFollowup(t, c, 1, 3, "3", "surface", time.Minute)
	complianceScore(t, c, 1, 4, "4", time.Minute, 2)
	complianceFollowup(t, c, 1, 4, "4", "surface", time.Minute)
	// Duplicate labels affect event volume, never exposure counts.
	complianceFollowup(t, c, 1, 4, "4", "surface", 2*time.Minute)
	complianceFollowup(t, c, 1, 5, "5", "surface", 48*time.Hour)
	refreshCompliance(t, c)
	assertComplianceCounts(t, c, map[string]int64{
		"delivered_exposures": 5, "delivered_agents": 1, "identifiable_exposures": 5, "mature_exposures": 5,
		"scored_1h_exposures": 3, "scored_24h_exposures": 3, "scored_48h_exposures": 3,
		"mature_batches": 5, "fully_scored_batches": 3, "unscored_batches": 2,
		"surfaced_exposures": 3, "surfaced_unscored_exposures": 1, "surfaced_nonpositive_exposures": 1,
		"positive_exposures": 2, "positive_surfaced_exposures": 1, "positive_batches": 2, "positive_batches_without_surface": 1,
		"feedback_events": 4, "feedback_events_with_impression": 4, "feedback_events_exact": 4,
		"surface_events": 4, "surface_events_with_impression": 4, "surface_events_exact": 4,
	})
	assertComplianceConservation(t, c)
}

func TestPostgresCompliancePM(t *testing.T) {
	c := complianceDB(t)
	for i := 1; i <= 7; i++ {
		complianceDelivery(t, c, i, 1, i, "pm", complianceServed)
		complianceFollowup(t, c, 1, i, "pm", "question", time.Minute)
		initiator, kind := 1, "broadcast"
		if i == 2 {
			initiator = 4
		}
		if i == 3 {
			kind = "friend"
		}
		complianceExec(t, c, `INSERT INTO conversations VALUES($1,1,4,$2,$3,$1)`, i, initiator, kind)
		after := time.Minute
		if i == 4 {
			after = 48 * time.Hour
		}
		if i == 5 {
			after = -time.Minute
		}
		complianceExec(t, c, `INSERT INTO private_messages VALUES($1,$2,1,$3)`, i*10, i, complianceServed.Add(after).UnixMilli())
		reply := 2 * time.Minute
		if i == 6 {
			reply = 48 * time.Hour
		}
		complianceExec(t, c, `INSERT INTO private_messages VALUES($1,$2,4,$3)`, i*10+1, i, complianceServed.Add(reply).UnixMilli())
	}
	// A later message cannot turn an old conversation into a newly initiated PM.
	complianceExec(t, c, `INSERT INTO private_messages VALUES(59,5,1,$1)`, complianceServed.Add(time.Minute).UnixMilli())
	complianceExec(t, c, `UPDATE conversations SET participant_a=4,participant_b=1 WHERE conv_id=7`)
	refreshCompliance(t, c)
	assertComplianceCounts(t, c, map[string]int64{
		"delivered_exposures": 7, "delivered_agents": 1, "identifiable_exposures": 7, "mature_exposures": 7,
		"mature_batches": 1, "unscored_batches": 1, "followup_exposures": 7,
		"followup_agent_pm_exposures": 3, "agent_pm_exposures": 3, "author_replied_exposures": 2, "silent_agents": 1,
	})
}

func TestPostgresComplianceAttributionAndBoundaries(t *testing.T) {
	c := complianceDB(t)
	for i, imp := range []string{"", "duplicate", "duplicate", "before", "search", "valid", "wrong-agent", "wrong-item"} {
		item := 10 + i
		if imp == "duplicate" {
			item = 20
		}
		complianceDelivery(t, c, i+1, 1, item, imp, complianceServed)
		after := time.Minute
		if imp == "before" {
			after = -time.Minute
		}
		agent := 1
		if imp == "wrong-agent" {
			agent = 2
		}
		if imp == "wrong-item" {
			item = 99
		}
		complianceScore(t, c, agent, item, imp, after, 1)
		complianceFollowup(t, c, agent, item, imp, "surface", after)
	}
	complianceExec(t, c, `UPDATE replay_logs SET request_mode='search' WHERE impression_id='search'`)
	for _, agent := range []int{3, 5, 6} {
		imp := fmt.Sprint("internal", agent)
		complianceDelivery(t, c, 10+agent, agent, 10, imp, complianceServed)
		complianceScore(t, c, agent, 10, imp, time.Minute, 1)
		complianceFollowup(t, c, agent, 10, imp, "surface", time.Minute)
	}
	// Delivery and event bucketing use Shanghai midnight; upper boundaries are exclusive.
	midnight := instant("2026-10-03T00:00:00+08:00")
	complianceDelivery(t, c, 30, 1, 10, "midnight", midnight)
	complianceScore(t, c, 1, 10, "midnight", 12*time.Hour, 1)
	complianceFollowup(t, c, 1, 10, "midnight", "surface", 12*time.Hour)
	refreshCompliance(t, c)
	assertComplianceCounts(t, c, map[string]int64{
		"delivered_exposures": 7, "delivered_agents": 1, "identifiable_exposures": 4, "mature_exposures": 4,
		"scored_1h_exposures": 1, "scored_24h_exposures": 1, "scored_48h_exposures": 1,
		"mature_batches": 4, "fully_scored_batches": 1, "unscored_batches": 3,
		"surfaced_exposures": 1, "positive_exposures": 1, "positive_surfaced_exposures": 1, "positive_batches": 1,
		"feedback_events": 8, "feedback_events_with_impression": 7, "feedback_events_exact": 2,
		"surface_events": 8, "surface_events_with_impression": 7, "surface_events_exact": 2,
	})
	assertComplianceReconciliation(t, c, complianceDay, complianceCutoff)
	// Refresh a new day first 1 ms before maturity, then exactly at maturity.
	for _, delta := range []time.Duration{-time.Millisecond, 0} {
		if err := RefreshComplianceDay(context.Background(), c, "2026-10-03", midnight.Add(48*time.Hour+delta)); err != nil {
			t.Fatal(err)
		}
		var delivered, mature, scored int
		err := c.QueryRow(`SELECT delivered_exposures,mature_exposures,scored_48h_exposures FROM feedback_compliance_daily WHERE day='2026-10-03' AND dimension='all'`).Scan(&delivered, &mature, &scored)
		want := 0
		if delta == 0 {
			want = 1
		}
		if err != nil || delivered != 1 || mature != want || scored != want {
			t.Fatal(delivered, mature, scored, err)
		}
	}
}

func TestPostgresComplianceCohortsAndConservation(t *testing.T) {
	c := complianceDB(t)
	for agent := 101; agent <= 119; agent++ {
		complianceExec(t, c, `INSERT INTO agents VALUES($1,$2,false)`, agent, fmt.Sprintf("c%d@example.invalid", agent))
		runtime, mode, cli, model, revision, contract := "rare", "skill", "0.1", "rare", "r1", "v1"
		var auto any = true
		switch {
		case agent >= 115:
			runtime, mode = "hermes", "skill"
		case agent >= 110:
			runtime, mode, cli, model, revision, contract, auto = "", "", "", "", "", "", nil
			if agent == 110 {
				runtime, mode, cli, model = "  ", "PLUGIN", "  ", "  "
			}
		case agent >= 105:
			runtime, mode, cli, model, revision, contract, auto = " OpenClaw ", "plugin", " 0.6.10 ", strings.Repeat("M", 80), strings.Repeat("r", 80), strings.Repeat("v", 80), false
		}
		if agent != 114 {
			complianceExec(t, c, `INSERT INTO agent_settings VALUES($1,$2,$3,$4,$5,$6,$7,$8)`, agent, runtime, mode, cli, model, revision, contract, auto)
		}
		imp := fmt.Sprint(agent)
		complianceDelivery(t, c, agent, agent, 10, imp, complianceServed)
		complianceScore(t, c, agent, 10, imp, time.Minute, 1)
		complianceFollowup(t, c, agent, 10, imp, "surface", time.Minute)
	}
	// Two exposures for one consumer cannot promote a four-Agent value.
	complianceDelivery(t, c, 200, 101, 20, "101", complianceServed)
	complianceScore(t, c, 101, 20, "101", time.Minute, 1)
	for agent, runtime := range map[int]string{201: "OpenClaw", 202: "event-only"} {
		complianceExec(t, c, `INSERT INTO agents VALUES($1,$2,false)`, agent, fmt.Sprintf("e%d@example.invalid", agent))
		complianceExec(t, c, `INSERT INTO agent_settings(agent_id,runtime_name) VALUES($1,$2)`, agent, runtime)
		complianceScore(t, c, agent, 10, "missing", time.Minute, 1)
		complianceFollowup(t, c, agent, 10, "missing", "surface", time.Minute)
	}
	refreshCompliance(t, c)
	all := complianceCounts(t, c, "all", "all")
	if all["delivered_agents"] != 19 || all["delivered_exposures"] != 20 || all["feedback_events"] != 22 {
		t.Fatal(all)
	}
	for _, tc := range []struct {
		dim, value                 string
		agents, deliveries, events int64
	}{
		{"runtime_name", "other", 4, 5, 6}, {"runtime_name", "openclaw", 5, 5, 6}, {"runtime_name", "hermes", 5, 5, 5}, {"runtime_name", "unknown", 5, 5, 5},
		{"runtime_mode", "skill", 9, 10, 10}, {"runtime_mode", "plugin", 5, 5, 5}, {"runtime_mode", "unknown", 5, 5, 7},
		{"cli_version", "0.6.10", 5, 5, 5}, {"model", strings.Repeat("m", 64), 5, 5, 5},
		{"skill_revision", strings.Repeat("r", 64), 5, 5, 5}, {"heartbeat_contract_version", strings.Repeat("v", 64), 5, 5, 5},
		{"auto_comment", "false", 5, 5, 5}, {"auto_comment", "true", 9, 10, 10},
	} {
		got := complianceCounts(t, c, tc.dim, tc.value)
		if got["delivered_agents"] != tc.agents || got["delivered_exposures"] != tc.deliveries || got["feedback_events"] != tc.events {
			t.Errorf("%s/%s: %v", tc.dim, tc.value, got)
		}
	}
	// Four empty settings plus the missing settings row contribute to unknown in every dimension.
	for _, dim := range []string{"runtime_name", "runtime_mode", "cli_version", "model", "skill_revision", "heartbeat_contract_version", "auto_comment"} {
		got := complianceCounts(t, c, dim, "unknown")
		if got["delivered_agents"] != 5 {
			t.Errorf("%s unknown: %v", dim, got)
		}
	}
	var disclosed int
	if err := c.QueryRow(`SELECT count(*) FROM feedback_compliance_daily WHERE dimension<>'all' AND value<>'other' AND delivered_agents<5`).Scan(&disclosed); err != nil || disclosed != 0 {
		t.Fatal(disclosed, err)
	}
	assertComplianceConservation(t, c)
	assertComplianceReconciliation(t, c, complianceDay, complianceCutoff)
}

func complianceSnapshot(t *testing.T, c *sql.DB) string {
	t.Helper()
	var snapshot string
	if err := c.QueryRow(`SELECT jsonb_agg(to_jsonb(d) ORDER BY day,dimension,value)::text FROM feedback_compliance_daily d`).Scan(&snapshot); err != nil {
		t.Fatal(err)
	}
	return snapshot
}

func TestPostgresComplianceWriters(t *testing.T) {
	c := complianceDB(t)
	for agent := 101; agent <= 105; agent++ {
		complianceExec(t, c, `INSERT INTO agents VALUES($1,$2,false)`, agent, fmt.Sprintf("c%d@example.invalid", agent))
		complianceExec(t, c, `INSERT INTO agent_settings(agent_id,runtime_name) VALUES($1,'before')`, agent)
		complianceDelivery(t, c, agent, agent, 10, fmt.Sprint(agent), complianceServed)
	}
	refreshCompliance(t, c)
	before := complianceSnapshot(t, c)
	refreshCompliance(t, c)
	if got := complianceSnapshot(t, c); got != before {
		t.Fatal("same cutoff is not idempotent")
	}
	complianceExec(t, c, `UPDATE agent_settings SET runtime_name='after'`)
	if err := RefreshComplianceDay(context.Background(), c, complianceDay, complianceCutoff.Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := complianceSnapshot(t, c); got != before {
		t.Fatal("stale writer replaced snapshot")
	}
	refreshCompliance(t, c)
	got := complianceCounts(t, c, "runtime_name", "after")
	if got["delivered_agents"] != 5 {
		t.Fatal(got)
	}
	var obsolete int
	if err := c.QueryRow(`SELECT count(*) FROM feedback_compliance_daily WHERE value='before'`).Scan(&obsolete); err != nil || obsolete != 0 {
		t.Fatal(obsolete, err)
	}
	before = complianceSnapshot(t, c)
	complianceExec(t, c, `CREATE FUNCTION reject_compliance() RETURNS trigger LANGUAGE plpgsql AS $$ BEGIN
 IF NEW.dimension='runtime_name' THEN RAISE EXCEPTION 'fixture interruption'; END IF; RETURN NEW; END $$;
 CREATE TRIGGER reject_compliance BEFORE INSERT ON feedback_compliance_daily FOR EACH ROW EXECUTE FUNCTION reject_compliance();`)
	if err := RefreshComplianceDay(context.Background(), c, complianceDay, complianceCutoff.Add(time.Hour)); err == nil {
		t.Fatal("interrupted refresh succeeded")
	}
	if got := complianceSnapshot(t, c); got != before {
		t.Fatal("failed insert did not roll back deletion")
	}
	tx, err := c.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err := tx.Exec(`SELECT pg_advisory_xact_lock(118,20261002)`); err != nil {
		t.Fatal(err)
	}
	if err := RefreshComplianceDay(context.Background(), c, complianceDay, complianceCutoff); err == nil {
		t.Fatal("competing writer succeeded")
	}
	if err := RefreshComplianceDay(context.Background(), c, "2026-02-30", complianceCutoff); err == nil {
		t.Fatal("invalid date accepted")
	}
}

func TestPostgresCompliancePendingDaysAndEmpty(t *testing.T) {
	c := complianceDB(t)
	ctx := context.Background()
	cutoff := instant("2026-10-09T12:00:00+08:00")
	check := func(want string) {
		t.Helper()
		days, err := CompliancePendingDays(ctx, c, cutoff)
		if err != nil || strings.Join(days, ",") != want {
			t.Fatal(days, err, want)
		}
	}
	check("2026-10-09,2026-10-08,2026-10-06,2026-10-05")
	if err := RefreshComplianceDay(ctx, c, "2026-10-06", instant("2026-10-08T23:59:59+08:00")); err != nil {
		t.Fatal(err)
	}
	check("2026-10-09,2026-10-08,2026-10-06,2026-10-05")
	if err := RefreshComplianceDay(ctx, c, "2026-10-06", cutoff); err != nil {
		t.Fatal(err)
	}
	check("2026-10-09,2026-10-08,2026-10-05,2026-10-04")
	refreshCompliance(t, c)
	assertComplianceCounts(t, c, map[string]int64{})
	var rows int
	if err := c.QueryRow(`SELECT count(*) FROM feedback_compliance_daily WHERE day=$1`, complianceDay).Scan(&rows); err != nil || rows != 1 {
		t.Fatal(rows, err)
	}
	// An unrelated cohort row does not establish that the all snapshot exists.
	complianceExec(t, c, `UPDATE feedback_compliance_daily SET dimension='runtime_name',value='other' WHERE day='2026-10-06'`)
	check("2026-10-09,2026-10-08,2026-10-06,2026-10-05")
	complianceExec(t, c, `INSERT INTO feedback_compliance_daily
 SELECT (jsonb_populate_record(NULL::feedback_compliance_daily,
 to_jsonb(seed)||jsonb_build_object('day',d::date,'snapshot_at',$1::timestamptz))).*
 FROM feedback_compliance_daily seed CROSS JOIN generate_series(timestamp '2026-09-09',timestamp '2026-10-06',interval '1 day') d
 WHERE seed.day='2026-10-02' AND seed.dimension='all'
 ON CONFLICT(day,dimension,value) DO UPDATE SET snapshot_at=EXCLUDED.snapshot_at`, cutoff)
	check("2026-10-09,2026-10-08")
	complianceExec(t, c, `DELETE FROM feedback_compliance_daily WHERE day='2026-09-09'`)
	check("2026-10-09,2026-10-08,2026-09-09")
}

func TestComplianceClassificationContract(t *testing.T) {
	// Pin the complete shared prefix, including exact duplicate attribution and
	// search events, so changes cannot silently invalidate reconciliation.
	want, _, ok := strings.Cut(refreshDaySQL, "), day_deliveries AS MATERIALIZED (")
	got, _, gotOK := strings.Cut(complianceDaySQL, "), day_deliveries AS MATERIALIZED (")
	if !ok || !gotOK || got != want {
		t.Fatal("compliance attribution differs from recommendation effect")
	}
}

func TestPostgresComplianceHalfOpenWindows(t *testing.T) {
	c := complianceDB(t)
	for i, after := range []time.Duration{0, time.Hour, 24 * time.Hour, 48 * time.Hour} {
		complianceDelivery(t, c, i+1, 1, 10+i, "boundary", complianceServed)
		complianceScore(t, c, 1, 10+i, "boundary", after, 0)
	}
	refreshCompliance(t, c)
	got := complianceCounts(t, c, "all", "all")
	if got["scored_1h_exposures"] != 1 || got["scored_24h_exposures"] != 2 || got["scored_48h_exposures"] != 3 {
		t.Fatal(got)
	}
}

func complianceDiscardedRows(t *testing.T, c *sql.DB) float64 {
	t.Helper()
	tx, err := c.Begin()
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	var raw []byte
	if err := tx.QueryRow("EXPLAIN (ANALYZE, FORMAT JSON) "+complianceDaySQL, complianceDay, complianceCutoff.UnixMilli()).Scan(&raw); err != nil {
		t.Fatal(err)
	}
	var plans []struct{ Plan map[string]any }
	if err := json.Unmarshal(raw, &plans); err != nil || len(plans) != 1 {
		t.Fatal(err, len(plans))
	}
	var walk func(map[string]any) float64
	walk = func(node map[string]any) float64 {
		loops, _ := node["Actual Loops"].(float64)
		filtered, _ := node["Rows Removed by Filter"].(float64)
		joinFiltered, _ := node["Rows Removed by Join Filter"].(float64)
		total := (filtered + joinFiltered) * loops
		children, _ := node["Plans"].([]any)
		for _, child := range children {
			total += walk(child.(map[string]any))
		}
		return total
	}
	return walk(plans[0].Plan)
}

func TestPostgresComplianceManyAgentsDay(t *testing.T) {
	c := complianceDB(t)
	served := complianceServed.UnixMilli()
	complianceExec(t, c, `
	 INSERT INTO agents SELECT n,'many'||n||'@example.invalid',false FROM generate_series(1000,4999) n;
	 INSERT INTO agent_settings(agent_id,runtime_name,mode,cli_version,model,skill_revision,heartbeat_contract_version,auto_comment)
	 SELECT n,'runtime-'||n%9,CASE WHEN n%2=0 THEN 'plugin' ELSE 'skill' END,'0.6.'||n%11,'model-'||n%7,'rev-'||n%13,'v'||n%3,n%2=0
	 FROM generate_series(1000,4999) n;`)
	// Each Agent receives two five-item pages; whole pages are scored or unscored.
	complianceExec(t, c, `INSERT INTO replay_logs SELECT 10000+n,1000+n%4000,10+n/4000,'many-'||n%4000||'-'||n/20000,$1::bigint+n,
	 true,'broadcast','need_search_v1',2,'recommendation',0,'{"search":{"context":{"input_origin":"agent_context"}}}'
	 FROM generate_series(0,39999) n`, served)
	complianceExec(t, c, `
	 INSERT INTO feedback_logs(agent_id,item_id,impression_id,feedback_at,score)
	 SELECT agent_id,item_id,CASE WHEN id%5=1 THEN '' ELSE impression_id END,served_at+1000,id%4-1 FROM replay_logs WHERE id%5<>0;
	 INSERT INTO followup_labels SELECT agent_id,item_id,impression_id,served_at+2000,'surface' FROM replay_logs WHERE id%10=0;
	 ANALYZE agents; ANALYZE agent_settings; ANALYZE replay_logs; ANALYZE feedback_logs; ANALYZE followup_labels;`)
	// Discarded rows depend on plan shape, not machine speed. Quadratic joins of
	// per-Agent aggregates or time-window scans in exposure lookups discard
	// hundreds of millions of rows at this size.
	discarded := complianceDiscardedRows(t, c)
	t.Logf("refresh plan discarded %.0f rows", discarded)
	if discarded > 1_000_000 {
		t.Fatalf("refresh plan discarded %.0f rows", discarded)
	}
	started := time.Now()
	refreshCompliance(t, c)
	t.Logf("4,000 Agents / 40,000 deliveries refreshed in %s", time.Since(started))
	got := complianceCounts(t, c, "all", "all")
	if got["delivered_agents"] != 4000 || got["delivered_exposures"] != 40000 || got["mature_exposures"] != 40000 ||
		got["scored_48h_exposures"] != 24000 || got["scored_48h_inferred_exposures"] != 8000 ||
		got["fully_scored_batches"] != 4800 || got["unscored_batches"] != 3200 || got["unscored_batches_with_inferred"] != 1600 ||
		got["feedback_events"] != 32000 || got["feedback_events_with_impression"] != 24000 || got["feedback_events_inferred"] != 8000 || got["silent_agents"] != 800 || got["surfaced_exposures"] != 4000 || got["mature_batches"] != 8000 {
		t.Fatal(got)
	}
	assertComplianceConservation(t, c)
}

func TestPostgresComplianceEventOnlyDayAndSchema(t *testing.T) {
	c := complianceDB(t)
	complianceScore(t, c, 1, 10, "missing", time.Minute, 1)
	complianceFollowup(t, c, 1, 10, "missing", "surface", time.Minute)
	refreshCompliance(t, c)
	assertComplianceCounts(t, c, map[string]int64{"feedback_events": 1, "feedback_events_with_impression": 1, "surface_events": 1, "surface_events_with_impression": 1})
	assertComplianceConservation(t, c)
	var rows, privateColumns int
	var barrier bool
	if err := c.QueryRow(`SELECT count(*) FROM feedback_compliance_daily`).Scan(&rows); err != nil || rows != 8 {
		t.Fatal(rows, err)
	}
	if err := c.QueryRow(`SELECT count(*) FROM information_schema.columns WHERE table_schema=current_schema()
 AND table_name='grafana_feedback_compliance_daily' AND column_name IN ('agent_id','item_id','impression_id','email','content')`).Scan(&privateColumns); err != nil || privateColumns != 0 {
		t.Fatal(privateColumns, err)
	}
	if err := c.QueryRow(`SELECT 'security_barrier=true'=ANY(reloptions) FROM pg_class WHERE oid='grafana_feedback_compliance_daily'::regclass`).Scan(&barrier); err != nil || !barrier {
		t.Fatal(barrier, err)
	}
	for _, set := range []string{"scored_1h_exposures=1", "fully_scored_batches=1", "degenerate_batches=1", "surfaced_unscored_exposures=1", "positive_exposures=1", "positive_batches_without_surface=1", "followup_agent_pm_exposures=1", "author_replied_exposures=1", "feedback_events_exact=2", "surface_events_exact=2", "delivered_agents=-1", "value=''", "schema_version=2", "dimension='email'", "scored_48h_inferred_exposures=1", "unscored_batches_with_inferred=1", "feedback_events_inferred=1", "silent_agents=1"} {
		if _, err := c.Exec(`UPDATE feedback_compliance_daily SET ` + set + ` WHERE dimension='all'`); err == nil {
			t.Errorf("invalid count or dimension accepted: %s", set)
		}
	}
}

func TestPostgresComplianceInferred(t *testing.T) {
	for _, tc := range []struct {
		name  string
		setup func(*testing.T, *sql.DB)
		want  map[string]int64
	}{
		{"unique", func(t *testing.T, c *sql.DB) { complianceScore(t, c, 1, 10, "", 5*time.Minute, 0) }, map[string]int64{"scored_48h_inferred_exposures": 1, "unscored_batches_with_inferred": 1, "feedback_events": 1, "feedback_events_inferred": 1}},
		{"ambiguous", func(t *testing.T, c *sql.DB) {
			complianceDelivery(t, c, 2, 1, 10, "b", complianceServed.Add(time.Hour))
			complianceScore(t, c, 1, 10, "", 2*time.Hour, 0)
		}, map[string]int64{"delivered_exposures": 2, "identifiable_exposures": 2, "mature_exposures": 2, "mature_batches": 2, "unscored_batches": 2, "feedback_events": 1}},
		{"boundaries", func(t *testing.T, c *sql.DB) {
			complianceScore(t, c, 1, 10, "", -time.Minute, 0)
			complianceScore(t, c, 1, 10, "", 48*time.Hour, 0)
		}, map[string]int64{"feedback_events": 1}},
		{"exact_precedence", func(t *testing.T, c *sql.DB) {
			complianceScore(t, c, 1, 10, "a", time.Minute, 0)
			complianceScore(t, c, 1, 10, "", 2*time.Minute, 0)
		}, map[string]int64{"scored_1h_exposures": 1, "scored_24h_exposures": 1, "scored_48h_exposures": 1, "fully_scored_batches": 1, "unscored_batches": 0, "feedback_events": 2, "feedback_events_with_impression": 1, "feedback_events_exact": 1, "feedback_events_inferred": 1}},
		{"search", func(t *testing.T, c *sql.DB) {
			complianceExec(t, c, `UPDATE replay_logs SET request_mode='search'`)
			complianceScore(t, c, 1, 10, "", time.Minute, 0)
		}, map[string]int64{"delivered_exposures": 0, "delivered_agents": 0, "identifiable_exposures": 0, "mature_exposures": 0, "mature_batches": 0, "unscored_batches": 0, "feedback_events": 1, "feedback_events_inferred": 1}},
		{"silent", func(t *testing.T, c *sql.DB) {}, map[string]int64{"silent_agents": 1}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			c := complianceDB(t)
			complianceDelivery(t, c, 1, 1, 10, "a", complianceServed)
			tc.setup(t, c)
			refreshCompliance(t, c)
			want := map[string]int64{"delivered_exposures": 1, "delivered_agents": 1, "identifiable_exposures": 1, "mature_exposures": 1, "mature_batches": 1, "unscored_batches": 1}
			for k, v := range tc.want {
				want[k] = v
			}
			assertComplianceCounts(t, c, want)
			assertComplianceConservation(t, c)
			assertComplianceReconciliation(t, c, complianceDay, complianceCutoff)
		})
	}
}

func TestPostgresComplianceInferredCrossDay(t *testing.T) {
	c := complianceDB(t)
	complianceDelivery(t, c, 1, 1, 10, "a", instant("2026-10-01T23:50:00+08:00"))
	complianceScore(t, c, 1, 10, "", -12*time.Hour+5*time.Minute, 0)
	refreshCompliance(t, c)
	assertComplianceCounts(t, c, map[string]int64{"feedback_events": 1, "feedback_events_inferred": 1})
	assertComplianceConservation(t, c)
	if err := RefreshComplianceDay(context.Background(), c, "2026-10-01", instant("2026-10-04T00:00:00+08:00")); err != nil {
		t.Fatal(err)
	}
	complianceExec(t, c, `DELETE FROM feedback_compliance_daily WHERE day=$1`, complianceDay)
	complianceExec(t, c, `UPDATE feedback_compliance_daily SET day=$1`, complianceDay)
	assertComplianceCounts(t, c, map[string]int64{"delivered_exposures": 1, "delivered_agents": 1, "identifiable_exposures": 1, "mature_exposures": 1, "mature_batches": 1, "unscored_batches": 1, "scored_48h_inferred_exposures": 1, "unscored_batches_with_inferred": 1})
	assertComplianceConservation(t, c)
}

func TestPostgresComplianceSilentWindowAndInternal(t *testing.T) {
	for _, after := range []time.Duration{59 * time.Hour, 60 * time.Hour} {
		t.Run(after.String(), func(t *testing.T) {
			c := complianceDB(t)
			complianceDelivery(t, c, 1, 1, 10, "a", complianceServed)
			complianceDelivery(t, c, 2, 2, 10, "b", complianceServed)
			complianceScore(t, c, 2, 99, "unmatched", after, 0)
			for _, agent := range []int{3, 5, 6} {
				complianceDelivery(t, c, agent, agent, 10, "internal", complianceServed)
				complianceScore(t, c, agent, 10, "", time.Minute, 1)
			}
			refreshCompliance(t, c)
			silent := int64(1)
			if after == 60*time.Hour {
				silent = 2
			}
			assertComplianceCounts(t, c, map[string]int64{"delivered_exposures": 2, "delivered_agents": 2, "identifiable_exposures": 2, "mature_exposures": 2, "mature_batches": 2, "unscored_batches": 2, "silent_agents": silent})
			assertComplianceConservation(t, c)
		})
	}
}
