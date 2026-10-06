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

// PendingDays prioritizes today, then the oldest observation in a bounded
// 31-day window. Missing days sort first; a failed day does not starve others.
func PendingDays(ctx context.Context, conn *sql.DB, cutoff time.Time) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, `
		WITH days AS (
		 SELECT (timezone('Asia/Shanghai', $1::timestamptz)::date-n) AS day,
		        timezone('Asia/Shanghai', $1::timestamptz)::date AS today
		 FROM generate_series(0,30) n
		)
		SELECT d.day::text FROM days d
		LEFT JOIN recommendation_effect_daily s USING(day)
        LEFT JOIN LATERAL (
          SELECT count(*) AS hour_rows,min(snapshot_at) AS snapshot_at
          FROM recommendation_effect_hourly h
          WHERE h.hour_start >= d.day::timestamp AT TIME ZONE 'Asia/Shanghai'
            AND h.hour_start < (d.day+1)::timestamp AT TIME ZONE 'Asia/Shanghai'
        ) h ON true
        GROUP BY d.day,d.today,h.hour_rows,h.snapshot_at
		ORDER BY (d.day=d.today) DESC,
		 CASE WHEN count(s.day)=28 AND (d.day=d.today OR h.hour_rows=672)
          THEN LEAST(min(s.snapshot_at),h.snapshot_at) END NULLS FIRST,d.day DESC
		LIMIT 6`, cutoff)
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
