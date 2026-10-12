package recommendationmetrics

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"
)

//go:embed match_day.sql
var matchDaySQL string

// MatchFinalDays is how long a day's mutual count can still grow: the scorer's
// message may follow the score by up to one day and the author's reply may
// follow that message by up to three days, so a day is final after day+5.
const MatchFinalDays = 5

// matchCatchupPerBatch bounds historical catch-up work per cron batch. A
// production day takes well under a second, so this fills history in hours.
const matchCatchupPerBatch = 8

// MatchPendingDays returns today and the previous MatchFinalDays Shanghai days,
// plus up to matchCatchupPerBatch older days that are missing or not yet
// final, starting from the earliest retained feedback day.
func MatchPendingDays(ctx context.Context, conn *sql.DB, cutoff time.Time) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, `WITH bounds AS (
 SELECT timezone('Asia/Shanghai',$1::timestamptz)::date today,
 (to_timestamp(min(feedback_at)/1000.0) AT TIME ZONE 'Asia/Shanghai')::date first_day FROM feedback_logs
 ), days AS (SELECT generate_series(first_day::timestamp,today::timestamp,interval '1 day')::date AS day,today FROM bounds)
 SELECT day::text FROM (
 SELECT day FROM days WHERE day>=today-$3::int
 UNION
 (SELECT d.day FROM days d LEFT JOIN match_overview_daily s USING(day)
 WHERE d.day<d.today-$3::int AND (s.day IS NULL OR s.snapshot_at<(d.day+$3::int)::timestamp AT TIME ZONE 'Asia/Shanghai')
 ORDER BY d.day DESC LIMIT $2)) pending ORDER BY day DESC`, cutoff, matchCatchupPerBatch, MatchFinalDays)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var days []string
	for rows.Next() {
		var day string
		if err = rows.Scan(&day); err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

// RefreshMatchDay stores one day's anonymous match totals. One locked
// transaction per date bounds retries, stale writers and partial failure.
func RefreshMatchDay(ctx context.Context, conn *sql.DB, day string, cutoff time.Time) error {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return fmt.Errorf("invalid match overview day: %w", err)
	}
	loc := time.FixedZone("CST", 8*3600)
	if d.Format("2006-01-02") > cutoff.In(loc).Format("2006-01-02") {
		return fmt.Errorf("match overview day is in the future")
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL statement_timeout='15s'; SET LOCAL lock_timeout='1s'"); err != nil {
		return err
	}
	var acquired bool
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(118,to_char($1::date,'YYYYMMDD')::integer)`, day).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("match overview day %s is already refreshing", day)
	}
	if _, err = tx.ExecContext(ctx, matchDaySQL, day, cutoff.UnixMilli()); err != nil {
		return fmt.Errorf("refresh match overview day %s: %w", day, err)
	}
	return tx.Commit()
}
