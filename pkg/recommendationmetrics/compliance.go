package recommendationmetrics

import (
	"context"
	"database/sql"
	_ "embed"
	"fmt"
	"time"
)

//go:embed compliance_day.sql
var complianceDaySQL string

// CompliancePendingDays returns today and yesterday, then at most
// FinalCatchUpLimit missing or non-final days from day-3 through day-30.
// The all row is always written, including on days with no activity.
func CompliancePendingDays(ctx context.Context, conn *sql.DB, cutoff time.Time) ([]string, error) {
	ctx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	rows, err := conn.QueryContext(ctx, `
	 WITH today AS (SELECT timezone('Asia/Shanghai',$1::timestamptz)::date AS d)
	 SELECT day::text FROM (
	  SELECT t.d-n AS day FROM today t, generate_series(0,1) n
	  UNION ALL
	  (SELECT t.d-n AS day FROM today t CROSS JOIN generate_series(3,30) n
	   LEFT JOIN feedback_compliance_daily s ON s.day=t.d-n AND s.dimension='all' AND s.value='all'
	   WHERE s.day IS NULL OR s.snapshot_at<(t.d-n+3)::timestamp AT TIME ZONE 'Asia/Shanghai'
	   ORDER BY day DESC LIMIT $2)
	 ) days ORDER BY day DESC`, cutoff, FinalCatchUpLimit)
	if err != nil {
		return nil, fmt.Errorf("select feedback compliance days: %w", err)
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

// RefreshComplianceDay replaces a day's complete cohort set atomically. The
// transaction lock also serializes manual backfills against the cron writer.
func RefreshComplianceDay(ctx context.Context, conn *sql.DB, day string, cutoff time.Time) error {
	return refreshComplianceDay(ctx, conn, day, cutoff, false)
}

// FinalizeMatureComplianceDay uses the existing per-day lock for racing workers.
// Recheck maturity under the lock so normal polling never recomputes a final day.
func FinalizeMatureComplianceDay(ctx context.Context, conn *sql.DB, cutoff time.Time) error {
	day := cutoff.In(time.FixedZone("UTC+8", 8*3600)).AddDate(0, 0, -3).Format("2006-01-02")
	return refreshComplianceDay(ctx, conn, day, cutoff, true)
}

func refreshComplianceDay(ctx context.Context, conn *sql.DB, day string, cutoff time.Time, onlyMissing bool) error {
	if _, err := time.Parse("2006-01-02", day); err != nil {
		return fmt.Errorf("invalid feedback compliance day: %w", err)
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
		return fmt.Errorf("feedback compliance day %s is already refreshing", day)
	}
	if onlyMissing {
		var final bool
		if err = tx.QueryRowContext(ctx, `SELECT EXISTS (SELECT FROM feedback_compliance_daily
    WHERE day=$1::date AND dimension='all' AND value='all'
    AND snapshot_at >= (day+3)::timestamp AT TIME ZONE 'Asia/Shanghai')`, day).Scan(&final); err != nil {
			return err
		}
		if final {
			return tx.Commit()
		}
	}
	var newer bool
	if err = tx.QueryRowContext(ctx, `SELECT COALESCE(max(snapshot_at)>to_timestamp($2::bigint/1000.0),false)
	 FROM feedback_compliance_daily WHERE day=$1::date`, day, cutoff.UnixMilli()).Scan(&newer); err != nil {
		return err
	}
	if newer {
		return tx.Commit()
	}
	if _, err = tx.ExecContext(ctx, `DELETE FROM feedback_compliance_daily WHERE day=$1::date`, day); err != nil {
		return err
	}
	if _, err = tx.ExecContext(ctx, complianceDaySQL, day, cutoff.UnixMilli()); err != nil {
		return fmt.Errorf("refresh feedback compliance day %s: %w", day, err)
	}
	return tx.Commit()
}
