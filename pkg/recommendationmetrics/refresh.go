// Package recommendationmetrics builds anonymous daily and hourly observations without
// changing recommendation, feedback ingestion, or private context storage.
package recommendationmetrics

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"
)

//go:embed refresh_day.sql
var refreshDaySQL string

// FinalCatchUpLimit bounds how many unfinalized past days one batch refreshes.
const FinalCatchUpLimit = 2

// PendingDays returns the Shanghai days one batch refreshes: today and
// yesterday (still accumulating activity), then at most FinalCatchUpLimit
// unfinalized days from day-3 back through day-30, newest first.
//
// A delivery day D is final once a snapshot's cutoff reaches Shanghai midnight
// at D+3: every exposure's 48-hour outcome window has closed and every event
// bucketed into D has occurred. Final days are never selected again, so normally
// the only historical work is the day that crossed day+3 at midnight. A day with
// a missing or incomplete grid, or an earlier snapshot, is caught up on the next
// batch, which closes gaps after missed runs or deploys.
func PendingDays(ctx context.Context, conn *sql.DB, cutoff time.Time) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, `
		WITH today AS (SELECT timezone('Asia/Shanghai', $1::timestamptz)::date AS d)
		SELECT day::text FROM (
		 SELECT t.d-n AS day FROM today t, generate_series(0,1) n
		 UNION ALL
		 (SELECT d.day FROM (
		  SELECT t.d-n AS day FROM today t, generate_series(3,30) n
		 ) d
		 LEFT JOIN LATERAL (
		  SELECT count(*) AS day_rows,min(snapshot_at) AS snapshot_at
		  FROM recommendation_effect_daily s WHERE s.day=d.day
		 ) s ON true
		 LEFT JOIN LATERAL (
		  SELECT count(*) AS hour_rows,min(snapshot_at) AS snapshot_at
		  FROM recommendation_effect_hourly h
		  WHERE h.hour_start >= d.day::timestamp AT TIME ZONE 'Asia/Shanghai'
		    AND h.hour_start < (d.day+1)::timestamp AT TIME ZONE 'Asia/Shanghai'
		 ) h ON true
		 WHERE s.day_rows<>28 OR h.hour_rows<>672
		    OR LEAST(s.snapshot_at,h.snapshot_at) < (d.day+3)::timestamp AT TIME ZONE 'Asia/Shanghai'
		  ORDER BY d.day DESC
		  LIMIT $2)
		) days ORDER BY day DESC`, cutoff, FinalCatchUpLimit)
	if err != nil {
		return nil, fmt.Errorf("select recommendation observation days: %w", err)
	}
	defer rows.Close()
	var days []string
	for rows.Next() {
		var day string
		if err := rows.Scan(&day); err != nil {
			return nil, err
		}
		days = append(days, day)
	}
	return days, rows.Err()
}

// RefreshDay commits daily and hourly dimension rows together. SQL timeouts and
// an advisory transaction lock bound concurrent replicas even if their Redis lease is lost.
// Older cutoffs cannot overwrite newer observations.
func RefreshDay(ctx context.Context, conn *sql.DB, day string, cutoff time.Time) error {
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return fmt.Errorf("invalid observation day: %w", err)
	}
	ctx, cancel := context.WithTimeout(ctx, 20*time.Second)
	defer cancel()
	tx, err := conn.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL statement_timeout = '15s'; SET LOCAL lock_timeout = '1s'"); err != nil {
		return err
	}
	var acquired bool
	err = tx.QueryRowContext(ctx, `SELECT pg_try_advisory_xact_lock(113, to_char($1::date,'YYYYMMDD')::integer)`, day).Scan(&acquired)
	if err != nil {
		return err
	}
	if !acquired {
		return fmt.Errorf("observation day %s is already refreshing", day)
	}
	if _, err = tx.ExecContext(ctx, refreshDaySQL, day, cutoff.UnixMilli()); err != nil {
		return fmt.Errorf("refresh recommendation observation %s: %w", day, err)
	}
	return tx.Commit()
}
