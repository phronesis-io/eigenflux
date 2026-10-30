package migrations_test

import (
	"context"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	"github.com/jackc/pgx/v5"
)

func TestCommissionObservabilityViewsPostgres(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" && cfg.Host != "::1" {
		t.Fatal("loopback PostgreSQL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	exec := func(query string, args ...interface{}) {
		t.Helper()
		if _, err := tx.Exec(ctx, query, args...); err != nil {
			t.Fatal(err)
		}
	}
	schema := fmt.Sprintf("commission_observability_%d", time.Now().UnixNano())
	exec("CREATE SCHEMA " + schema)
	exec("SET LOCAL search_path TO " + schema + ",public")
	exec(`CREATE TABLE telemetry_events_v2 (event_type text, properties jsonb,event_at bigint);
CREATE TABLE notification_inbox (source_type text,event_kind text,received_at bigint,expires_at bigint,acknowledged_at bigint)`)
	up, _, _ := strings.Cut(migration(t, "000123_commission_observability_views.sql"), "-- +goose Down")
	exec(up)
	exec(`INSERT INTO telemetry_events_v2 VALUES ('maintenance_attempt',
'{"component":"dispatch","phase":"commission","result":"succeeded","duration_ms":50,"commission":{"job_kind":"commission_fulfillment","stage":"agent","adapter":"acp"}}',
(extract(epoch FROM now())*1000)::bigint)`)
	// Unrecognized client-controlled dimensions cannot leak into anonymous views.
	exec(`INSERT INTO telemetry_events_v2 VALUES ('maintenance_attempt',
'{"component":"dispatch","phase":"commission","result":"succeeded","commission":{"job_kind":"private-content","stage":"private-content"}}',
(extract(epoch FROM now())*1000)::bigint)`)
	var events, durations, durationSum int64
	if err := tx.QueryRow(ctx, `SELECT sum(event_count),sum(duration_count),sum(duration_ms_sum) FROM grafana_commission_dispatch_daily`).Scan(&events, &durations, &durationSum); err != nil || events != 1 || durations != 1 || durationSum != 50 {
		t.Fatalf("anonymous execution aggregate: events=%d durations=%d sum=%d err=%v", events, durations, durationSum, err)
	}
	exec(`INSERT INTO notification_inbox SELECT 'commission_order','order.state.changed.v1',n-30000,n+60000,NULL FROM (SELECT (extract(epoch FROM now())*1000)::bigint n) b;
INSERT INTO notification_inbox SELECT 'commission_order','order.state.changed.v1',n-30000,n+60000,n-15000 FROM (SELECT (extract(epoch FROM now())*1000)::bigint n) b;
INSERT INTO notification_inbox SELECT 'commission_order','order.state.changed.v1',n-60000,n-1000,NULL FROM (SELECT (extract(epoch FROM now())*1000)::bigint n) b;
INSERT INTO notification_inbox SELECT 'commission_order','order.state.changed.v1',n-3000000001,n,n-3000000000 FROM (SELECT (extract(epoch FROM now())*1000)::bigint n) b`)
	var pending, acked, latencySum, latencyCount int64
	var oldest float64
	if err := tx.QueryRow(ctx, `SELECT pending_count,oldest_pending_age_seconds,acked_count,ack_latency_ms_sum,ack_latency_count FROM grafana_commission_notification_delivery`).Scan(&pending, &oldest, &acked, &latencySum, &latencyCount); err != nil || pending != 1 || oldest != 30 || acked != 1 || latencyCount != 1 || latencySum != 15000 {
		t.Fatalf("ACK semantics: pending=%d age=%v acked=%d sum=%d count=%d err=%v", pending, oldest, acked, latencySum, latencyCount, err)
	}
	var barriers int
	if err := tx.QueryRow(ctx, `SELECT count(*) FROM pg_class WHERE relnamespace=$1::regnamespace AND relname IN ('grafana_commission_dispatch_daily','grafana_commission_notification_delivery') AND 'security_barrier=true'=ANY(reloptions)`, schema).Scan(&barriers); err != nil || barriers != 2 {
		t.Fatalf("aggregate security barrier: %d %v", barriers, err)
	}
	_, down, _ := strings.Cut(migration(t, "000123_commission_observability_views.sql"), "-- +goose Down")
	exec(down)
}
