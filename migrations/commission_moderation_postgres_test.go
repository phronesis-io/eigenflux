package migrations_test

import (
	"context"
	"fmt"
	"github.com/jackc/pgx/v5"
	"os"
	"strings"
	"testing"
	"time"
)

func TestModerationInboxMigrationPostgres(t *testing.T) {
	dsn := os.Getenv("MODERATION_NOTIFICATION_PG_TEST_DSN")
	if dsn == "" {
		t.Skip("MODERATION_NOTIFICATION_PG_TEST_DSN required")
	}
	cfg, err := pgx.ParseConfig(dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Host != "127.0.0.1" && cfg.Host != "localhost" {
		t.Fatal("loopback PostgreSQL required")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	c, err := pgx.ConnectConfig(ctx, cfg)
	if err != nil {
		t.Fatal(err)
	}
	defer c.Close(ctx)
	tx, err := c.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(context.Background())
	exec := func(sql string) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	schema := fmt.Sprintf("moderation_inbox_%d", time.Now().UnixNano())
	exec("CREATE SCHEMA " + schema + ";SET LOCAL search_path TO " + schema)
	exec("CREATE TABLE agents(agent_id BIGINT PRIMARY KEY);INSERT INTO agents VALUES(1)")
	baseline, _, _ := strings.Cut(migration(t, "000103_commission_order_notification_inbox.sql"), "-- +goose Down")
	exec(baseline)
	up, down, _ := strings.Cut(migration(t, "000131_commission_moderation_notifications.sql"), "-- +goose Down")
	exec(up)
	exec(down)
	exec(up)
	insert := func(id int, source string, orderID, version int) string {
		return fmt.Sprintf("INSERT INTO notification_inbox(notification_id,agent_id,source_type,source_id,dedupe_key,event_kind,order_id,order_version,payload_json,occurred_at,received_at,expires_at) VALUES(%d,1,'%s',%d,'event:%d','test',%d,%d,'{}',1000,1001,2000)", id, source, id, id, orderID, version)
	}
	exec(insert(1, "commission_order", 5, 1))
	exec(insert(2, "commission_moderation", 0, 0))
	for _, sql := range []string{insert(3, "commission_order", 0, 0), insert(3, "commission_moderation", 5, 1), insert(3, "unknown", 0, 0), down} {
		exec("SAVEPOINT invalid")
		if _, err := tx.Exec(ctx, sql); err == nil {
			t.Fatal("source-specific constraint or history-preserving downgrade accepted invalid state")
		}
		exec("ROLLBACK TO SAVEPOINT invalid")
	}
	var count int
	if err := tx.QueryRow(ctx, "SELECT count(*) FROM notification_inbox").Scan(&count); err != nil || count != 2 {
		t.Fatalf("history lost: %d %v", count, err)
	}
}
