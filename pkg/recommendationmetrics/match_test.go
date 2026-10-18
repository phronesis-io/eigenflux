package recommendationmetrics

import (
	"context"
	"database/sql"
	"os"
	"strings"
	"testing"
	"time"
)

func applyMatchMigration(t *testing.T, c *sql.DB) {
	t.Helper()
	b, err := os.ReadFile("../../migrations/000118_match_overview_daily.sql")
	if err != nil {
		t.Fatal(err)
	}
	up, _, _ := strings.Cut(string(b), "-- +goose Down")
	if _, err = c.Exec(`CREATE TABLE conversations(conv_id bigint PRIMARY KEY,participant_a bigint,participant_b bigint,
	 origin_type text,origin_id bigint,UNIQUE(participant_a,participant_b,origin_id));
	 CREATE TABLE private_messages(msg_id bigserial PRIMARY KEY,conv_id bigint,sender_id bigint,created_at bigint);` + up); err != nil {
		t.Fatal(err)
	}
}

type matchRow struct{ scored, good, pgcScored, pgcGood, ugcScored, ugcGood, messaged, mutual int }

func readMatchDay(t *testing.T, c *sql.DB, day string) matchRow {
	t.Helper()
	var r matchRow
	if err := c.QueryRow(`SELECT scored_pairs,good_pairs,pgc_scored_pairs,pgc_good_pairs,ugc_scored_pairs,ugc_good_pairs,
	 ugc_messaged_pairs,ugc_mutual_pairs FROM grafana_match_overview_daily WHERE day=$1`, day).
		Scan(&r.scored, &r.good, &r.pgcScored, &r.pgcGood, &r.ugcScored, &r.ugcGood, &r.messaged, &r.mutual); err != nil {
		t.Fatal(err)
	}
	return r
}

func TestPostgresMatchOverviewDay(t *testing.T) {
	c := fixtureDB(t)
	applyMatchMigration(t, c)
	ctx := context.Background()
	// Fixture authors: 3 PGC, 4 UGC, 5 official, 6 bot; item 50 has no known author.
	if _, err := c.Exec(`INSERT INTO raw_items VALUES (21,4),(24,4),(25,1)`); err != nil {
		t.Fatal(err)
	}
	at := func(v string) int64 { return instant("2026-07-" + v + "+08:00").UnixMilli() }
	score := func(agent, item int64, ms int64, s int) {
		t.Helper()
		if _, err := c.Exec(`INSERT INTO feedback_logs(agent_id,item_id,impression_id,feedback_at,score) VALUES($1,$2,'',$3,$4)`, agent, item, ms, s); err != nil {
			t.Fatal(err)
		}
	}
	conv := func(id, a, b, item int64, origin string) {
		t.Helper()
		if _, err := c.Exec(`INSERT INTO conversations VALUES($1,LEAST($2::bigint,$3::bigint),GREATEST($2::bigint,$3::bigint),$4,$5)`, id, a, b, origin, item); err != nil {
			t.Fatal(err)
		}
	}
	msg := func(convID, sender int64, ms int64) {
		t.Helper()
		if _, err := c.Exec(`INSERT INTO private_messages(conv_id,sender_id,created_at) VALUES($1,$2,$3)`, convID, sender, ms); err != nil {
			t.Fatal(err)
		}
	}
	score(1, 10, at("10T00:00:00"), 2) // PGC good at Shanghai midnight
	conv(90, 1, 3, 10, "broadcast")    // PGC conversations never count as mutual
	msg(90, 1, at("10T00:01:00"))
	msg(90, 3, at("10T00:02:00"))
	score(1, 20, at("10T09:00:00"), 0) // one pair: neutral first, good later
	score(1, 20, at("10T10:00:00"), 2)
	conv(100, 1, 4, 20, "broadcast") // messaged right after the good score, reply in 1h: mutual
	msg(100, 1, at("10T10:00:30"))
	msg(100, 4, at("10T11:00:00"))
	score(1, 21, at("10T10:00:00"), 1)
	conv(101, 1, 4, 21, "broadcast") // reply after 72h: messaged only
	msg(101, 1, at("10T10:01:00"))
	msg(101, 4, at("13T10:01:01"))
	score(2, 20, at("10T12:00:00"), 2)
	conv(102, 2, 4, 20, "broadcast") // first message 25h after the score: not linked
	msg(102, 2, at("11T13:00:00"))
	msg(102, 4, at("11T14:00:00"))
	score(2, 24, at("10T15:00:00"), 2)
	conv(103, 2, 4, 24, "broadcast") // message 30 minutes before the score still counts
	msg(103, 2, at("10T14:30:00"))
	msg(103, 4, at("10T16:00:00"))
	conv(104, 2, 4, 0, "friend") // friend conversations are not about a broadcast
	msg(104, 2, at("10T15:01:00"))
	msg(104, 4, at("10T15:02:00"))
	score(2, 50, at("10T16:00:00"), 1)  // unknown author: totals only
	score(2, 30, at("10T17:00:00"), -1) // official author: totals only
	score(2, 40, at("10T18:00:00"), 9)  // invalid score
	score(5, 20, at("10T18:00:00"), 2)  // official consumer
	score(1, 25, at("10T19:00:00"), 2)  // own broadcast: UGC good, never messaged
	score(1, 20, at("11T00:00:00"), 2)  // next day

	// At 11:30 only the morning is visible; the 10:01 conversation has no reply yet.
	if err := RefreshMatchDay(ctx, c, "2026-07-10", instant("2026-07-10T11:30:00+08:00")); err != nil {
		t.Fatal(err)
	}
	if got := readMatchDay(t, c, "2026-07-10"); got != (matchRow{3, 3, 1, 1, 2, 2, 2, 1}) {
		t.Fatalf("partial day: %+v", got)
	}
	final := instant("2026-07-15T00:00:00+08:00")
	if err := RefreshMatchDay(ctx, c, "2026-07-10", final); err != nil {
		t.Fatal(err)
	}
	if got := readMatchDay(t, c, "2026-07-10"); got != (matchRow{8, 7, 1, 1, 5, 5, 3, 2}) {
		t.Fatalf("final day: %+v", got)
	}
	// An older cutoff never overwrites a newer snapshot.
	if err := RefreshMatchDay(ctx, c, "2026-07-10", final.Add(-time.Millisecond)); err != nil {
		t.Fatal(err)
	}
	if got := readMatchDay(t, c, "2026-07-10"); got != (matchRow{8, 7, 1, 1, 5, 5, 3, 2}) {
		t.Fatalf("stale overwrite: %+v", got)
	}
	if err := RefreshMatchDay(ctx, c, "2026-07-16", final); err == nil {
		t.Fatal("future accepted")
	}
	tx, err := c.BeginTx(ctx, nil)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback()
	if _, err = tx.Exec(`SELECT pg_advisory_xact_lock(118,20260710)`); err != nil {
		t.Fatal(err)
	}
	if err = RefreshMatchDay(ctx, c, "2026-07-10", final.Add(time.Second)); err == nil {
		t.Fatal("competing writer accepted")
	}
}

func TestPostgresMatchOverviewCatchup(t *testing.T) {
	c := fixtureDB(t)
	applyMatchMigration(t, c)
	ctx := context.Background()
	cutoff := instant("2026-10-09T12:00:00+08:00")
	if _, err := c.Exec(`INSERT INTO feedback_logs(agent_id,feedback_at,score) VALUES(1,$1,1)`, instant("2026-09-20T12:00:00+08:00").UnixMilli()); err != nil {
		t.Fatal(err)
	}
	days, err := MatchPendingDays(ctx, c, cutoff)
	if err != nil {
		t.Fatal(err)
	}
	// Today plus five unfinished days, then eight older missing days.
	if strings.Join(days, ",") != "2026-10-09,2026-10-08,2026-10-07,2026-10-06,2026-10-05,2026-10-04,2026-10-03,2026-10-02,2026-10-01,2026-09-30,2026-09-29,2026-09-28,2026-09-27,2026-09-26" {
		t.Fatal(days)
	}
	// 10-03 becomes final at 10-08 00:00; a snapshot taken before that stays pending.
	if err = RefreshMatchDay(ctx, c, "2026-10-03", cutoff); err != nil {
		t.Fatal(err)
	}
	if err = RefreshMatchDay(ctx, c, "2026-10-02", instant("2026-10-06T12:00:00+08:00")); err != nil {
		t.Fatal(err)
	}
	if days, err = MatchPendingDays(ctx, c, cutoff); err != nil {
		t.Fatal(err)
	}
	if strings.Join(days, ",") != "2026-10-09,2026-10-08,2026-10-07,2026-10-06,2026-10-05,2026-10-04,2026-10-02,2026-10-01,2026-09-30,2026-09-29,2026-09-28,2026-09-27,2026-09-26,2026-09-25" {
		t.Fatal(days)
	}
}
