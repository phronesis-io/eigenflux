package recommendationmetrics

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"
)

//go:embed history_day.sql
var historyDaySQL string

// HistoryPendingDays refreshes recent event days and catches up two missing
// historical days per batch. Completed aggregates are retained indefinitely.
func HistoryPendingDays(ctx context.Context, conn *sql.DB, cutoff time.Time) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, `WITH bounds AS (
 SELECT timezone('Asia/Shanghai',$1::timestamptz)::date today,
 (to_timestamp(min(feedback_at)/1000.0) AT TIME ZONE 'Asia/Shanghai')::date first_day FROM feedback_logs
 ), days AS (SELECT generate_series(first_day::timestamp,today::timestamp,interval '1 day')::date AS day,today FROM bounds)
 SELECT day::text FROM (
 SELECT day FROM days WHERE day>=today-2
 UNION
 (SELECT d.day FROM days d LEFT JOIN feedback_history_daily h USING(day)
 WHERE d.day<d.today-2 AND (h.day IS NULL OR h.snapshot_at<(d.day+3)::timestamp AT TIME ZONE 'Asia/Shanghai')
 ORDER BY d.day DESC LIMIT 2)) pending ORDER BY day DESC`, cutoff)
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

// RefreshHistoryDay preserves raw-score history independently of replay attribution.
// One locked transaction per date bounds retries, stale writers and partial failure.
func RefreshHistoryDay(ctx context.Context, conn *sql.DB, day string, cutoff time.Time) error {
	d, err := time.Parse("2006-01-02", day)
	if err != nil {
		return fmt.Errorf("invalid history day: %w", err)
	}
	loc := time.FixedZone("CST", 8*3600)
	if d.Format("2006-01-02") > cutoff.In(loc).Format("2006-01-02") {
		return fmt.Errorf("history day is in the future")
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
	if err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(116,to_char($1::date,'YYYYMMDD')::integer)`, day).Scan(&acquired); err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("history day %s is already refreshing", day)
	}
	if _, err = tx.ExecContext(ctx, historyDaySQL, day, cutoff.UnixMilli()); err != nil {
		return fmt.Errorf("refresh feedback history %s: %w", day, err)
	}
	return tx.Commit()
}
