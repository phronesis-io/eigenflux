package consolev2retention

import (
	"context"
	"database/sql"
	"fmt"
	"os"
	"testing"
	"time"

	"gorm.io/driver/postgres"
	"gorm.io/gorm"
)

func TestPostgresCommandExpiryDoesNotFenceLiveLease(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL retention semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `CREATE TEMP TABLE agent_commands (
		command_id bigint PRIMARY KEY, status text NOT NULL, created_at bigint NOT NULL,
		completed_at bigint NULL, claim_owner_runtime_id text NULL,
		claim_token_hash text NULL, claim_until bigint NULL
	) ON COMMIT PRESERVE ROWS`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	old := now - 31*DayMS
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO agent_commands
		(command_id, status, created_at, claim_owner_runtime_id, claim_token_hash, claim_until)
		VALUES (1, 'claimed', $1, 'live', 'live-token', $2),
		       (2, 'claimed', $1, 'stale', 'stale-token', $3),
		       (3, 'pending', $1, NULL, NULL, NULL)`, old, now+60_000, now-1); err != nil {
		t.Fatal(err)
	}
	var expirySQL string
	for _, job := range Jobs() {
		if job.Name == "command_expiry" {
			expirySQL = job.SQL
			break
		}
	}
	if _, err := conn.ExecContext(context.Background(), expirySQL, 10); err != nil {
		t.Fatal(err)
	}
	rows, err := conn.QueryContext(context.Background(), `SELECT command_id, status,
		COALESCE(claim_owner_runtime_id, '') FROM agent_commands ORDER BY command_id`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	want := map[int64]struct {
		status string
		owner  string
	}{1: {"claimed", "live"}, 2: {"expired", ""}, 3: {"expired", ""}}
	for rows.Next() {
		var id int64
		var status, owner string
		if err := rows.Scan(&id, &status, &owner); err != nil {
			t.Fatal(err)
		}
		if expected := want[id]; status != expected.status || owner != expected.owner {
			t.Fatalf("command %d became (%s,%s), want (%s,%s)", id, status, owner, expected.status, expected.owner)
		}
		delete(want, id)
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
	if len(want) != 0 {
		t.Fatalf("missing command results: %#v", want)
	}
}

func TestPostgresExpiredCommandReopensUnexpiredAttention(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL retention semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `CREATE TEMP TABLE agent_attention_items (
		attention_id bigint PRIMARY KEY, agent_id bigint NOT NULL, producer text NOT NULL,
		status text NOT NULL, response_status text NOT NULL, selected_action_key text NULL,
		expires_at bigint NULL, updated_at bigint NOT NULL, item_revision bigint NOT NULL
	) ON COMMIT PRESERVE ROWS`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `CREATE TEMP TABLE agent_commands (
		command_id bigint PRIMARY KEY, agent_id bigint NOT NULL, attention_id bigint NULL,
		command_type text NOT NULL, status text NOT NULL
	) ON COMMIT PRESERVE ROWS`); err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO agent_attention_items
		(attention_id, agent_id, producer, status, response_status, selected_action_key, expires_at, updated_at, item_revision)
		VALUES (10, 20, 'agent', 'pending', 'pending', 'contact', $1, $2, 3),
		       (11, 20, 'agent', 'pending', 'pending', 'contact', $3, $2, 7)`, now+DayMS, now-31*DayMS, now-1); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO agent_commands(command_id, agent_id, attention_id, command_type, status)
		VALUES (100, 20, 10, 'attention_response', 'expired'),
		       (101, 20, 11, 'attention_response', 'expired')`); err != nil {
		t.Fatal(err)
	}
	var recoverySQL string
	for _, job := range Jobs() {
		if job.Name == "attention_command_expiry_recovery" {
			recoverySQL = job.SQL
			break
		}
	}
	if _, err := conn.ExecContext(context.Background(), recoverySQL, 10); err != nil {
		t.Fatal(err)
	}
	var status, response string
	var selected *string
	var revision int64
	if err := conn.QueryRowContext(context.Background(), `SELECT status, response_status,
		selected_action_key, item_revision FROM agent_attention_items WHERE attention_id=10`).
		Scan(&status, &response, &selected, &revision); err != nil {
		t.Fatal(err)
	}
	if status != "open" || response != "failed" || selected != nil || revision != 4 {
		t.Fatalf("recovered item=(%s,%s,%v,%d), want open/failed/nil/4", status, response, selected, revision)
	}
	if err := conn.QueryRowContext(context.Background(), `SELECT status FROM agent_attention_items WHERE attention_id=11`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("already expired Attention should remain pending for attention_expiry, got %s", status)
	}
}

func TestPostgresAttentionRedactionCompletesAcrossSmallBatches(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL retention semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	conn, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn.Close()
	if _, err := conn.ExecContext(context.Background(), `CREATE TEMP TABLE agent_attention_items (
		attention_id bigint PRIMARY KEY, producer text NOT NULL, generated_at bigint NOT NULL,
		redacted_at bigint NULL, title text NOT NULL, summary text NOT NULL,
		body text NOT NULL, recommendation text NOT NULL, updated_at bigint NOT NULL
	) ON COMMIT PRESERVE ROWS`); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `CREATE TEMP TABLE agent_commands (
		command_id bigint PRIMARY KEY, attention_id bigint NULL, command_type text NOT NULL,
		status text NOT NULL, payload jsonb NOT NULL
	) ON COMMIT PRESERVE ROWS`); err != nil {
		t.Fatal(err)
	}
	old := time.Now().UnixMilli() - 8*DayMS
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO agent_attention_items
		(attention_id,producer,generated_at,title,summary,body,recommendation,updated_at)
		VALUES (1,'agent',$1,'title','summary','body','recommendation',$1)`, old); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), `INSERT INTO agent_commands(command_id,attention_id,command_type,status,payload) VALUES
		(10,1,'attention_response','completed','{"attention_snapshot":{"title":"a","body":"b","recommendation":"c"}}'),
		(11,1,'attention_response','failed','{"attention_snapshot":{"title":"d","body":"e","recommendation":"f"}}')`); err != nil {
		t.Fatal(err)
	}
	jobs := map[string]string{}
	for _, job := range Jobs() {
		jobs[job.Name] = job.SQL
	}
	if _, err := conn.ExecContext(context.Background(), jobs["attention_command_payload_redaction"], 1); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), jobs["attention_text_redaction"], 1); err != nil {
		t.Fatal(err)
	}
	if _, err := conn.ExecContext(context.Background(), jobs["attention_command_payload_redaction"], 1); err != nil {
		t.Fatal(err)
	}
	var remaining int
	if err := conn.QueryRowContext(context.Background(), `SELECT count(*) FROM agent_commands
		WHERE COALESCE(payload #>> '{attention_snapshot,title}','') <> ''
		   OR COALESCE(payload #>> '{attention_snapshot,body}','') <> ''
		   OR COALESCE(payload #>> '{attention_snapshot,recommendation}','') <> ''`).Scan(&remaining); err != nil {
		t.Fatal(err)
	}
	if remaining != 0 {
		t.Fatalf("%d command snapshots escaped a later redaction batch", remaining)
	}
}

func TestPostgresAttentionExpirySkipsConcurrentResponse(t *testing.T) {
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN is required for PostgreSQL retention semantics")
	}
	gdb, err := gorm.Open(postgres.Open(dsn), &gorm.Config{})
	if err != nil {
		t.Fatal(err)
	}
	sqlDB, err := gdb.DB()
	if err != nil {
		t.Fatal(err)
	}
	schema := fmt.Sprintf("retention_race_%d", time.Now().UnixNano())
	if err := gdb.Exec("CREATE SCHEMA " + schema).Error; err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = gdb.Exec("DROP SCHEMA IF EXISTS " + schema + " CASCADE").Error })
	if err := gdb.Exec("CREATE TABLE " + schema + `.agent_attention_items (
		attention_id bigint PRIMARY KEY, agent_id bigint NOT NULL, producer text NOT NULL,
		status text NOT NULL, expires_at bigint NULL, response_status text NOT NULL,
		updated_at bigint NOT NULL, item_revision bigint NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	if err := gdb.Exec("CREATE TABLE " + schema + `.agent_commands (
		command_id bigint PRIMARY KEY, agent_id bigint NOT NULL, attention_id bigint NULL,
		status text NOT NULL
	)`).Error; err != nil {
		t.Fatal(err)
	}
	now := time.Now().UnixMilli()
	if err := gdb.Exec("INSERT INTO "+schema+`.agent_attention_items
		(attention_id,agent_id,producer,status,expires_at,response_status,updated_at,item_revision)
		VALUES (1,2,'agent','open',?,'none',?,1)`, now-1, now).Error; err != nil {
		t.Fatal(err)
	}
	conn1, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn1.Close()
	conn2, err := sqlDB.Conn(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	defer conn2.Close()
	for _, conn := range []*sql.Conn{conn1, conn2} {
		if _, err := conn.ExecContext(context.Background(), "SET search_path TO "+schema); err != nil {
			t.Fatal(err)
		}
	}
	tx, err := conn1.BeginTx(context.Background(), nil)
	if err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `SELECT attention_id FROM agent_attention_items
		WHERE attention_id=1 FOR UPDATE`); err != nil {
		t.Fatal(err)
	}
	var expirySQL string
	for _, job := range Jobs() {
		if job.Name == "attention_expiry" {
			expirySQL = job.SQL
			break
		}
	}
	done := make(chan error, 1)
	go func() {
		_, execErr := conn2.ExecContext(context.Background(), expirySQL, 10)
		done <- execErr
	}()
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("retention blocked behind the concurrent responder instead of SKIP LOCKED")
	}
	if _, err := tx.ExecContext(context.Background(), `INSERT INTO agent_commands
		(command_id,agent_id,attention_id,status) VALUES (10,2,1,'pending')`); err != nil {
		t.Fatal(err)
	}
	if _, err := tx.ExecContext(context.Background(), `UPDATE agent_attention_items
		SET status='pending' WHERE attention_id=1`); err != nil {
		t.Fatal(err)
	}
	if err := tx.Commit(); err != nil {
		t.Fatal(err)
	}
	if _, err := conn2.ExecContext(context.Background(), expirySQL, 10); err != nil {
		t.Fatal(err)
	}
	var status string
	if err := conn2.QueryRowContext(context.Background(), `SELECT status FROM agent_attention_items WHERE attention_id=1`).Scan(&status); err != nil {
		t.Fatal(err)
	}
	if status != "pending" {
		t.Fatalf("concurrent response was overwritten by expiry: status=%s", status)
	}
}
