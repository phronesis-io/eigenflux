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

func TestFriendConversationOpeningMigration(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for friend conversation migration tests")
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
	exec := func(query string) {
		t.Helper()
		if _, err := tx.Exec(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	schema := fmt.Sprintf("friend_opening_%d", time.Now().UnixNano())
	exec("CREATE SCHEMA " + schema)
	exec("SET LOCAL search_path TO " + schema)
	exec(`CREATE TABLE agents (agent_id bigint PRIMARY KEY);
		INSERT INTO agents VALUES (1),(2),(3),(4),(5);
		CREATE TABLE friend_requests (id bigint PRIMARY KEY, from_uid bigint, to_uid bigint, status smallint, greeting text, created_at bigint);
		INSERT INTO friend_requests VALUES (101,1,2,4,'first',100), (102,2,1,1,'second',101),
			(103,1,3,0,'not accepted',103), (104,2,3,2,'rejected',104), (105,1,4,3,'cancelled',105),
			(106,1,5,1,'',106), (107,1,6,1,'deleted account',107)`)
	up, down, _ := strings.Cut(migration(t, "000140_friend_conversation_openings.sql"), "-- +goose Down")
	exec(up)
	openingIDs := func() string {
		t.Helper()
		var ids string
		err := tx.QueryRow(ctx, `SELECT coalesce(string_agg(request_id::text, ',' ORDER BY request_id),'') FROM friend_conversation_openings`).Scan(&ids)
		if err != nil {
			t.Fatal(err)
		}
		return ids
	}
	if ids := openingIDs(); ids != "101" {
		t.Fatalf("unexpected backfilled IDs %q", ids)
	}
	exec(down)
	exec(up)
	exec("INSERT INTO friend_requests VALUES (108,1,6,1,'deleted peer',108)")
	exec("SAVEPOINT before_accept")
	exec("UPDATE friend_requests SET status=1 WHERE id=103")
	if ids := openingIDs(); ids != "101,103" {
		t.Fatalf("pending-to-accepted snapshot missing: %q", ids)
	}
	exec("ROLLBACK TO SAVEPOINT before_accept")
	if ids := openingIDs(); ids != "101" {
		t.Fatalf("snapshot survived failed acceptance transaction: %q", ids)
	}
	exec("UPDATE friend_requests SET id=201, greeting='replacement', status=0 WHERE id=101")
	exec("UPDATE friend_requests SET status=1 WHERE id=201")
	if ids := openingIDs(); ids != "101" {
		t.Fatalf("retry changed original greeting: %q", ids)
	}
	if _, err := tx.Exec(ctx, down); err == nil || !strings.Contains(err.Error(), "Cannot remove original greetings") {
		t.Fatalf("unexpected rollback result for sole remaining original: %v", err)
	}
}
