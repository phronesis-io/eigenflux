package recommendationmetrics

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
)

func applyStableMigration(t *testing.T, c *sql.DB) {
	t.Helper()
	b, err := os.ReadFile("../../migrations/000117_stable_agent_daily.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(b), "-- +goose Down")
	if _, err = c.Exec(up); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresStableAgentDay(t *testing.T) {
	c := fixtureDB(t)
	applyStableMigration(t, c)
	ctx := context.Background()
	if _, err := c.Exec(`INSERT INTO agents VALUES (7,'late@example.invalid',false),(8,'quiet@example.invalid',false)`); err != nil {
		t.Fatal(err)
	}
	at := func(v string) int64 { return instant(v).UnixMilli() }
	ins := func(agent, item int64, ms int64, score int) {
		t.Helper()
		if _, err := c.Exec(`INSERT INTO feedback_logs(agent_id,item_id,impression_id,feedback_at,score) VALUES($1,$2,'',$3,$4)`, agent, item, ms, score); err != nil {
			t.Fatal(err)
		}
	}
	// Agent 1: five look-back days, the last one just before Shanghai midnight.
	for _, d := range []string{"2026-07-03T10:00:00+08:00", "2026-07-04T10:00:00+08:00", "2026-07-05T10:00:00+08:00", "2026-07-07T10:00:00+08:00", "2026-07-09T23:59:00+08:00"} {
		ins(1, 20, at(d), 0)
	}
	// Agent 2: only four look-back days.
	for _, d := range []string{"2026-07-03T10:00:00+08:00", "2026-07-04T10:00:00+08:00", "2026-07-05T10:00:00+08:00", "2026-07-06T10:00:00+08:00"} {
		ins(2, 20, at(d), 1)
	}
	// Official Agent 5: every day, never stable.
	// Agent 7: five days, but one is outside the seven-day window and one is an invalid score.
	// Agent 8: stable, silent on the measured day.
	for _, d := range []string{"03", "04", "05", "06", "07", "08", "09"} {
		ins(5, 20, at("2026-07-"+d+"T10:00:00+08:00"), 1)
	}
	for _, d := range []string{"02", "04", "05", "06"} {
		ins(7, 20, at("2026-07-"+d+"T10:00:00+08:00"), 1)
	}
	ins(7, 20, at("2026-07-08T10:00:00+08:00"), 9)
	for _, d := range []string{"03", "04", "05", "06", "07"} {
		ins(8, 20, at("2026-07-"+d+"T10:00:00+08:00"), -1)
	}
	day := "2026-07-10T"
	ins(1, 10, at(day+"00:00:00+08:00"), 2) // PGC positive at midnight
	ins(1, 20, at(day+"01:00:00+08:00"), 1) // UGC positive
	ins(1, 20, at(day+"02:00:00+08:00"), 0) // UGC neutral
	ins(1, 30, at(day+"03:00:00+08:00"), 1) // official author: total only
	ins(1, 50, at(day+"04:00:00+08:00"), -1)
	ins(1, 50, at(day+"05:00:00+08:00"), 9) // invalid score
	ins(1, 20, at(day+"13:00:00+08:00"), 2) // after cutoff
	ins(2, 20, at(day+"06:00:00+08:00"), 2) // not stable
	ins(5, 20, at(day+"06:00:00+08:00"), 2) // official consumer
	cutoff := instant(day + "12:00:00+08:00")
	if err := RefreshStableDay(ctx, c, "2026-07-10", cutoff); err != nil {
		t.Fatal(err)
	}
	var stable, scoring, events, pos, pgcPos, ugcPos, pgcEvents, ugcEvents int
	read := func() {
		t.Helper()
		if err := c.QueryRow(`SELECT stable_agents,scoring_agents,feedback_events,positive_events,pgc_positive_events,ugc_positive_events,pgc_feedback_events,ugc_feedback_events FROM grafana_stable_agent_daily WHERE day='2026-07-10'`).Scan(&stable, &scoring, &events, &pos, &pgcPos, &ugcPos, &pgcEvents, &ugcEvents); err != nil {
			t.Fatal(err)
		}
	}
	read()
	if stable != 2 || scoring != 1 || events != 5 || pos != 3 || pgcPos != 1 || ugcPos != 1 || pgcEvents != 1 || ugcEvents != 2 {
		t.Fatalf("unexpected: stable=%d scoring=%d events=%d pos=%d pgcPos=%d ugcPos=%d pgc=%d ugc=%d", stable, scoring, events, pos, pgcPos, ugcPos, pgcEvents, ugcEvents)
	}
	// An older cutoff never overwrites a newer snapshot.
	if _, err := c.Exec(`DELETE FROM feedback_logs`); err != nil {
		t.Fatal(err)
	}
	if err := RefreshStableDay(ctx, c, "2026-07-10", cutoff.Add(-1)); err != nil {
		t.Fatal(err)
	}
	read()
	if stable != 2 || pos != 3 {
		t.Fatal("stale overwrite", stable, pos)
	}
	// A day with no stable Agents is an observed zero row.
	if err := RefreshStableDay(ctx, c, "2026-07-10", cutoff.Add(1)); err != nil {
		t.Fatal(err)
	}
	read()
	if stable != 0 || scoring != 0 || pos != 0 {
		t.Fatal("empty day", stable, scoring, pos)
	}
	if err := RefreshStableDay(ctx, c, "2026-07-11", cutoff); err == nil {
		t.Fatal("future accepted")
	}
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT pg_advisory_xact_lock(117,20260710)`); err != nil {
		t.Fatal(err)
	}
	if err = RefreshStableDay(ctx, c, "2026-07-10", cutoff.Add(2)); err == nil {
		t.Fatal("competing writer accepted")
	}
}

func TestPostgresStableAgentCatchup(t *testing.T) {
	c := fixtureDB(t)
	applyStableMigration(t, c)
	ctx := context.Background()
	cutoff := instant("2026-10-09T12:00:00+08:00")
	if _, err := c.Exec(`INSERT INTO feedback_logs(agent_id,feedback_at,score) VALUES(1,$1,1)`, instant("2026-09-20T12:00:00+08:00").UnixMilli()); err != nil {
		t.Fatal(err)
	}
	days, err := StablePendingDays(ctx, c, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Join(days, ",") != "2026-10-09,2026-10-08,2026-10-07,2026-10-06,2026-10-05,2026-10-04,2026-10-03,2026-10-02,2026-10-01,2026-09-30,2026-09-29" {
		t.Fatal(days)
	}
	if err = RefreshStableDay(ctx, c, "2026-10-06", cutoff); err != nil {
		t.Fatal(err)
	}
	if err = RefreshStableDay(ctx, c, "2026-09-27", cutoff); err != nil {
		t.Fatal(err)
	}
	days, err = StablePendingDays(ctx, c, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	// 10-06 is final; 09-27 is the first day with a full look-back window.
	if strings.Join(days, ",") != "2026-10-09,2026-10-08,2026-10-07,2026-10-05,2026-10-04,2026-10-03,2026-10-02,2026-10-01,2026-09-30,2026-09-29,2026-09-28" {
		t.Fatal(days)
	}
}
