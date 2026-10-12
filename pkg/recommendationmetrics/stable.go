package recommendationmetrics

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"
)

//go:embed stable_day.sql
var stableDaySQL string

// stableCatchupPerBatch bounds historical catch-up work per cron batch. A
// production day takes well under a second, so this fills 90 days in hours.
const stableCatchupPerBatch = 8

// StablePendingDays returns today and the previous two Shanghai days, plus up
// to stableCatchupPerBatch older days that are missing or not yet final. The
// first day considered is seven days after the earliest retained feedback,
// because earlier days cannot have a full look-back window.
func StablePendingDays(ctx context.Context, conn *sql.DB, cutoff time.Time) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, `WITH bounds AS (
 SELECT timezone('Asia/Shanghai',$1::timestamptz)::date today,
 (to_timestamp(min(feedback_at)/1000.0) AT TIME ZONE 'Asia/Shanghai')::date+7 first_day FROM feedback_logs
 ), days AS (SELECT generate_series(first_day::timestamp,today::timestamp,interval '1 day')::date AS day,today FROM bounds)
 SELECT day::text FROM (
 SELECT day FROM days WHERE day>=today-2
 UNION
 (SELECT d.day FROM days d LEFT JOIN stable_agent_daily s USING(day)
 WHERE d.day<d.today-2 AND (s.day IS NULL OR s.snapshot_at<(d.day+3)::timestamp AT TIME ZONE 'Asia/Shanghai')
 ORDER BY d.day DESC LIMIT $2)) pending ORDER BY day DESC`, cutoff, stableCatchupPerBatch)
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

// RefreshStableDay stores one day's anonymous stable-Agent totals. One locked
// transaction per date bounds retries, stale writers and partial failure.
func RefreshStableDay(ctx context.Context, conn *sql.DB, day string, cutoff time.Time) error {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return fmt.Errorf("invalid stable agent day: %w", err)
	}
	loc := time.FixedZone("CST", 8*3600)
	if d.Format("2006-01-02") > cutoff.In(loc).Format("2006-01-02") {
		return fmt.Errorf("stable agent day is in the future")
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
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(117,to_char($1::date,'YYYYMMDD')::integer)`, day).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("stable agent day %s is already refreshing", day)
	}
	if _, err = tx.ExecContext(ctx, stableDaySQL, day, cutoff.UnixMilli()); err != nil {
		return fmt.Errorf("refresh stable agent day %s: %w", day, err)
	}
	return tx.Commit()
}
