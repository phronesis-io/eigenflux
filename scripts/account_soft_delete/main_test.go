package main

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

// Run on a disposable, migrated loopback database with an explicitly
// authorized fixture login. Fixture rows include permanent audit records.
func TestPostgresAccountSoftDelete(t *testing.T) {
	dsn := os.Getenv("ACCOUNT_SOFT_DELETE_TEST_DSN")
	if dsn == "" {
		t.Skip("ACCOUNT_SOFT_DELETE_TEST_DSN required")
	}
	u, e := url.Parse(dsn)
	if e != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost") || !strings.HasSuffix(u.Path, "_test") {
		t.Fatal("isolated loopback *_test database required")
	}
	db, e := sql.Open("postgres", dsn)
	if e != nil {
		t.Fatal(e)
	}
	defer db.Close()
	ctx := context.Background()
	seed := time.Now().UnixNano()
	exec := func(q string, args ...any) {
		t.Helper()
		if _, err := db.Exec(q, args...); err != nil {
			t.Fatal(err)
		}
	}
	number := func(q string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRow(q, args...).Scan(&n); err != nil {
			t.Fatal(err)
		}
		return n
	}
	add := func(id int64, email, kind string) {
		exec("INSERT INTO agents(agent_id,email,email_kind,agent_name,created_at,updated_at) VALUES ($1,$2,$3,'fixture',1,1)", id, email, kind)
	}
	targetEmail := fmt.Sprintf("target-%d@fixture.example", seed)
	add(seed, targetEmail, "v2_bound")
	other := seed + 1
	otherEmail := fmt.Sprintf("other-%d@fixture.example", seed)
	add(other, otherEmail, "legacy_real")
	exec("INSERT INTO agent_email_bindings(agent_id,normalized_email,verification_state,status,created_at,updated_at) VALUES ($1,$2,'verified','active',1,1)", seed, targetEmail)
	for _, id := range []int64{seed, other} {
		exec("INSERT INTO agent_network_goals(agent_id,goal_text,source,status,created_at,updated_at) VALUES ($1,'preserve text','human_edit','active',1,1)", id)
		exec("INSERT INTO agent_sessions(agent_id,token_hash,status,expire_at,created_at,last_seen_at) VALUES ($1,$2,0,10,1,1)", id, fmt.Sprint(id))
	}
	req := request{agentID: seed, email: strings.ToUpper(targetEmail), ticket: "fixture-ticket"}
	preview, err := deactivate(ctx, db, req)
	if err != nil {
		t.Fatal(err)
	}
	if len(preview.changed) == 0 || len(preview.blocked) > 0 {
		t.Fatal(preview)
	}
	stillActive := func(id int64) bool {
		return number("SELECT count(*) FROM agents WHERE agent_id=$1 AND identity_state='active'", id) == 1
	}
	if !stillActive(seed) || number("SELECT count(*) FROM account_soft_delete_audit WHERE agent_id=$1", seed) != 0 {
		t.Fatal("preview wrote data")
	}
	req.apply = true
	wrong := req
	wrong.email = otherEmail
	if _, err = deactivate(ctx, db, wrong); err == nil || !stillActive(seed) {
		t.Fatal("mismatched account")
	}
	_, err = deactivate(ctx, db, req)
	if err != nil {
		t.Fatal(err)
	}
	if stillActive(seed) || !stillActive(other) || number("SELECT count(*) FROM agent_network_goals WHERE agent_id=$1 AND goal_text='preserve text' AND status='deleted'", seed) != 1 || number("SELECT count(*) FROM agent_network_goals WHERE agent_id=$1 AND status='active'", other) != 1 || number("SELECT count(*) FROM agent_sessions WHERE agent_id=$1 AND status=1", seed) != 1 || number("SELECT count(*) FROM agent_email_bindings WHERE agent_id=$1 AND status='revoked'", seed) != 1 {
		t.Fatal("unexpected account deactivation state")
	}
	if n := number("SELECT count(*) FROM account_soft_delete_audit WHERE agent_id=$1 AND operator_name=session_user AND ticket=$2", seed, req.ticket); n != 1 {
		t.Fatal("missing audit")
	}
	repeat, err := deactivate(ctx, db, req)
	if err != nil || !repeat.alreadyDeleted {
		t.Fatalf("idempotent retry: %v %v", repeat, err)
	}
	if _, err = deactivate(ctx, db, wrong); err == nil {
		t.Fatal("retry accepted wrong email")
	}

	// A failure in the final account update must roll back session changes and audit.
	add(seed+2, fmt.Sprintf("deleted-%d@identity.invalid", other), "internal_alias")
	if _, err = deactivate(ctx, db, request{agentID: other, email: otherEmail, apply: true, ticket: "collision"}); err == nil {
		t.Fatal("expected tombstone collision")
	}
	if !stillActive(other) || number("SELECT count(*) FROM agent_sessions WHERE agent_id=$1 AND status=0", other) != 1 || number("SELECT count(*) FROM account_soft_delete_audit WHERE agent_id=$1", other) != 0 {
		t.Fatal("partial deactivation after rollback")
	}

	exec("INSERT INTO trading_services(service_id,seller_agent_id,title,amount_atomic,delivery_deadline_ms,status,created_at,updated_at) VALUES ($1,$1,'offer',1,10,1,1,1)", other)
	blocked, err := deactivate(ctx, db, request{agentID: other, email: otherEmail, apply: true, ticket: "blocked"})
	if err == nil || len(blocked.blocked) != 1 || blocked.blocked[0] != (count{"trading_services", 1}) || !stillActive(other) {
		t.Fatalf("external dependency: %v %v", blocked, err)
	}
}
