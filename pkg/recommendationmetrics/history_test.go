package recommendationmetrics

import (
	"context"
	"os"
	"strings"
	"testing"
)

func TestPostgresHistoryLegacyVolumesAndFencing(t *testing.T) {
	c := fixtureDB(t)
	b, err := os.ReadFile("../../migrations/000116_feedback_history_daily.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(b), "-- +goose Down")
	if _, err = c.Exec(up); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cutoff := instant("2026-10-09T12:00:00+08:00")
	start := instant("2026-07-01T00:00:00+08:00").UnixMilli()
	_, err = c.Exec(`INSERT INTO feedback_logs(agent_id,item_id,impression_id,feedback_at,score) VALUES
 (1,10,'',$1,1),(1,10,'',$1,2),(2,20,'',$1,0),(5,30,'',$1,-1),(6,40,'',$1,9),(2,20,'',$1::bigint-1,2)`, start)
	if err != nil {
		t.Fatal(err)
	}
	if err = RefreshHistoryDay(ctx, c, "2026-07-01", cutoff); err != nil {
		t.Fatal(err)
	}
	var n, agents, pos, strong, neg, top, pgc, ugc int
	var unavailable bool
	err = c.QueryRow(`SELECT feedback_events,feedback_agents,score_1,score_2,score_neg1,top3_feedback_events,pgc_feedback_events,ugc_feedback_events,delivery_rows IS NULL FROM feedback_history_daily WHERE day='2026-07-01'`).Scan(&n, &agents, &pos, &strong, &neg, &top, &pgc, &ugc, &unavailable)
	if err != nil {
		t.Fatal(err)
	}
	if n != 4 || agents != 3 || pos != 1 || strong != 1 || neg != 1 || top != 4 || pgc != 2 || ugc != 1 || !unavailable {
		t.Fatalf("unexpected: %d %d %d %d %d %d %d %d %v", n, agents, pos, strong, neg, top, pgc, ugc, unavailable)
	}
	_, err = c.Exec(`INSERT INTO replay_logs(id,agent_id,served_at,delivered,source_kind,request_mode) VALUES(1,1,$1,true,'broadcast','feed'),(2,1,$1::bigint+86400000,true,'broadcast','feed'),(3,2,$1::bigint+86400000,true,'broadcast','search')`, start+3600000)
	if err != nil {
		t.Fatal(err)
	}
	for _, d := range []string{"2026-07-01", "2026-07-02", "2026-07-03"} {
		if err = RefreshHistoryDay(ctx, c, d, cutoff); err != nil {
			t.Fatal(err)
		}
	}
	var deliveries, receivers int
	if err = c.QueryRow(`SELECT delivery_rows,delivery_agents FROM feedback_history_daily WHERE day='2026-07-02'`).Scan(&deliveries, &receivers); err != nil {
		t.Fatal(err)
	}
	if deliveries != 1 || receivers != 1 {
		t.Fatal("search leaked", deliveries, receivers)
	}
	if err = c.QueryRow(`SELECT delivery_rows FROM feedback_history_daily WHERE day='2026-07-03'`).Scan(&deliveries); err != nil || deliveries != 0 {
		t.Fatal("empty day", deliveries, err)
	}
	if _, err = c.Exec(`DELETE FROM feedback_logs`); err != nil {
		t.Fatal(err)
	}
	if err = RefreshHistoryDay(ctx, c, "2026-07-01", cutoff.Add(-1)); err != nil {
		t.Fatal(err)
	}
	if err = c.QueryRow(`SELECT feedback_events FROM feedback_history_daily WHERE day='2026-07-01'`).Scan(&n); err != nil || n != 4 {
		t.Fatal("stale overwrite", n, err)
	}
	if err = RefreshHistoryDay(ctx, c, "2026-10-10", cutoff); err == nil {
		t.Fatal("future accepted")
	}
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT pg_advisory_xact_lock(116,20260701)`); err != nil {
		t.Fatal(err)
	}
	if err = RefreshHistoryDay(ctx, c, "2026-07-01", cutoff); err == nil {
		t.Fatal("competing writer accepted")
	}
	tx.Rollback()
}
func TestPostgresHistoryCatchup(t *testing.T) {
	c := fixtureDB(t)
	b, err := os.ReadFile("../../migrations/000116_feedback_history_daily.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(b), "-- +goose Down")
	if _, err = c.Exec(up); err != nil {
		t.Fatal(err)
	}
	ctx := context.Background()
	cutoff := instant("2026-10-09T12:00:00+08:00")
	if _, err = c.Exec(`INSERT INTO feedback_logs(agent_id,feedback_at,score) VALUES(1,$1,1)`, instant("2026-04-13T12:00:00+08:00").UnixMilli()); err != nil {
		t.Fatal(err)
	}
	days, err := HistoryPendingDays(ctx, c, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(days, ",") != "2026-10-09,2026-10-08,2026-10-07,2026-10-06,2026-10-05" {
		t.Fatal(days)
	}
	if err = RefreshHistoryDay(ctx, c, "2026-10-06", cutoff); err != nil {
		t.Fatal(err)
	}
	days, err = HistoryPendingDays(ctx, c, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(days, ",") != "2026-10-09,2026-10-08,2026-10-07,2026-10-05,2026-10-04" {
		t.Fatal(days)
	}
}
