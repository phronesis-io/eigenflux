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

func TestPrivateMessageIntentMigration(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for message intent migration tests")
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	conn, err := pgx.Connect(ctx, dsn)
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close(ctx)
	tx, err := conn.Begin(ctx)
	if err != nil {
		t.Fatal(err)
	}
	defer tx.Rollback(ctx)
	exec := func(sql string) {
		t.Helper()
		if _, err := tx.Exec(ctx, sql); err != nil {
			t.Fatal(err)
		}
	}
	schema := fmt.Sprintf("pm_intent_%d", time.Now().UnixNano())
	exec("CREATE SCHEMA " + schema)
	exec("SET LOCAL search_path TO " + schema)
	exec(`CREATE TABLE private_messages (msg_id bigint PRIMARY KEY); INSERT INTO private_messages VALUES (1)`)
	up, down, _ := strings.Cut(migration(t, "000146_private_message_intent.sql"), "-- +goose Down")
	exec(up)
	var unknown bool
	if err := tx.QueryRow(ctx, `SELECT message_kind IS NULL AND reply_to_message_id IS NULL FROM private_messages WHERE msg_id=1`).Scan(&unknown); err != nil || !unknown {
		t.Fatalf("history was classified: %v", err)
	}
	for _, values := range []string{"(2,NULL,NULL)", "(3,'contact',NULL)", "(4,'reply',1)"} {
		exec("INSERT INTO private_messages VALUES " + values)
	}
	for _, values := range []string{"(5,NULL,1)", "(5,'contact',1)", "(5,'reply',NULL)", "(5,'reply',0)", "(5,'other',NULL)"} {
		exec("SAVEPOINT bad_intent")
		if _, err := tx.Exec(ctx, "INSERT INTO private_messages VALUES "+values); err == nil {
			t.Fatalf("invalid intent accepted: %s", values)
		}
		exec("ROLLBACK TO SAVEPOINT bad_intent")
	}
	exec(down)
	exec(up)
}
