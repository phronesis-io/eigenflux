package testaccountreset

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"os"
	"strings"
	"testing"
	"time"

	_ "github.com/lib/pq"
)

// Every entry but the last two is a shape the guard must ignore.
var testPatterns = []string{"@pgc.eigenflux.one", "*@pgc.eigenflux.one", "news*@pgc.eigenflux.one",
	"???????@pgc.eigenflux.one", "news[!0-9]ot@pgc.eigenflux.one", "new[a-z]bot@pgc.eigenflux.one", "n[0-9]@pgc.eigenflux.one",
	"reset[0-9]@pgc.eigenflux.one", "reset[1-9][0-9]@pgc.eigenflux.one"}

var testGuard = Guard{TestPatterns: testPatterns, InternalSuffixes: []string{"@bot.eigenflux.one", "@pgc.eigenflux.one"}}

func TestAllowed(t *testing.T) {
	for email, want := range map[string]bool{
		"reset0@pgc.eigenflux.one":    true,
		" Reset42@PGC.eigenflux.one ": true,
		"reset100@pgc.eigenflux.one":  false,
		// Only literal-prefix-plus-digit-class entries qualify an address: the
		// PGC publishing fleet lives on the same domain.
		"newsbot@pgc.eigenflux.one": false,
		"n7@pgc.eigenflux.one":      false,
		"someone@example.com":       false,
		"":                          false,
	} {
		if got := testGuard.Allowed(email); got != want {
			t.Errorf("Allowed(%q) = %v, want %v", email, got, want)
		}
	}
	if (Guard{InternalSuffixes: testGuard.InternalSuffixes}).Allowed("reset0@pgc.eigenflux.one") {
		t.Error("empty pattern list must allow nothing")
	}
	// A narrow pattern for an outside address is a config mistake, not a test account.
	outside := Guard{TestPatterns: []string{"someone@example.com", "user[0-9]@example.com"}, InternalSuffixes: testGuard.InternalSuffixes}
	if outside.Allowed("someone@example.com") || outside.Allowed("user3@example.com") {
		t.Error("addresses outside the controlled domains must never be allowed")
	}
	if (Guard{TestPatterns: testPatterns}).Allowed("reset0@pgc.eigenflux.one") {
		t.Error("empty controlled-domain list must allow nothing")
	}
}

func openLoopback(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("PG_DSN")
	if dsn == "" {
		t.Skip("PG_DSN required")
	}
	if !strings.Contains(dsn, "127.0.0.1") && !strings.Contains(dsn, "localhost") {
		t.Fatal("loopback test database required")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { db.Close() })
	return db
}

// TestPostgresSchemaCoverage fails when a migration adds an agent-scoped column
// that the reset neither deletes, reports, nor deliberately ignores.
func TestPostgresSchemaCoverage(t *testing.T) {
	db := openLoopback(t)
	handled := map[string]bool{"agents": true}
	for _, s := range append(append(append([]Step{}, Steps...), Blocking...), ReportOnly...) {
		handled[s.Table] = true
	}
	// Not per-account state: a global backfill cursor.
	handled["console_v2_backfill_state"] = true
	// Removed indirectly: rows cascade from agents via agent_id, and from conversations via conv_id.
	handled["agent_email_bindings"] = true
	handled["private_messages"] = true

	rows, err := db.Query(`
		SELECT DISTINCT c.conrelid::regclass::text
		  FROM pg_constraint c
		 WHERE c.contype = 'f' AND c.confrelid = 'agents'::regclass AND c.confdeltype NOT IN ('c', 'n')
		UNION
		SELECT col.table_name::text
		  FROM information_schema.columns col
		  JOIN information_schema.tables t USING (table_schema, table_name)
		 WHERE col.table_schema = 'public' AND t.table_type = 'BASE TABLE'
		   AND col.data_type IN ('bigint', 'jsonb', 'character varying', 'text')
		   AND col.column_name ~ '(agent_id|agent_ids|_uid|^sender_id|^receiver_id|^participant_[ab]$|^email$|^normalized_email$)'
		   AND NOT EXISTS (
		         SELECT 1 FROM pg_constraint c
		           JOIN pg_attribute a ON a.attrelid = c.conrelid AND a.attnum = ANY (c.conkey)
		          WHERE c.contype = 'f' AND c.confdeltype IN ('c', 'n')
		            AND c.conrelid = format('%I.%I', col.table_schema, col.table_name)::regclass
		            AND a.attname = col.column_name)`)
	if err != nil {
		t.Fatal(err)
	}
	defer rows.Close()
	for rows.Next() {
		var table string
		if err := rows.Scan(&table); err != nil {
			t.Fatal(err)
		}
		if !handled[table] {
			t.Errorf("table %s holds per-agent rows that survive or block DELETE FROM agents; add it to Steps or ReportOnly", table)
		}
	}
	if err := rows.Err(); err != nil {
		t.Fatal(err)
	}
}

func TestPostgresReset(t *testing.T) {
	db := openLoopback(t)
	ctx := context.Background()
	now := time.Now().UnixMilli()
	base := time.Now().UnixNano()
	target, bystander := base, base+1
	// Two-digit suffix keeps the address inside testPatterns on every run.
	email := fmt.Sprintf("reset%d@pgc.eigenflux.one", 10+base%90)
	otherEmail := fmt.Sprintf("bystander-%d@example.com", base)

	exec := func(query string, args ...any) {
		t.Helper()
		if _, err := db.ExecContext(ctx, query, args...); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
	}
	count := func(query string, args ...any) int {
		t.Helper()
		var n int
		if err := db.QueryRowContext(ctx, query, args...).Scan(&n); err != nil {
			t.Fatalf("%s: %v", query, err)
		}
		return n
	}
	cleanup := func() {
		for _, mail := range []string{email, otherEmail} {
			db.ExecContext(ctx, `DELETE FROM raw_items WHERE author_agent_id IN (SELECT agent_id FROM agents WHERE email = $1)`, mail)
			db.ExecContext(ctx, `DELETE FROM agent_profiles WHERE agent_id IN (SELECT agent_id FROM agents WHERE email = $1)`, mail)
			db.ExecContext(ctx, `DELETE FROM agent_cli_account_switches WHERE source_agent_id IN (SELECT agent_id FROM agents WHERE email = $1)`, mail)
			db.ExecContext(ctx, `DELETE FROM trade_order_events WHERE actor_agent_id IN (SELECT agent_id FROM agents WHERE email = $1)`, mail)
			db.ExecContext(ctx, `DELETE FROM agents WHERE email = $1`, mail)
		}
	}
	cleanup()
	t.Cleanup(cleanup)

	for id, mail := range map[int64]string{target: email, bystander: otherEmail} {
		exec(`INSERT INTO agents (agent_id, email, agent_name, bio, created_at, updated_at) VALUES ($1, $2, 'used', 'used bio', $3, $3)`, id, mail, now)
		exec(`INSERT INTO agent_profiles (agent_id, status, keywords, updated_at) VALUES ($1, 3, 'ai', $2)`, id, now)
		exec(`INSERT INTO agent_settings (agent_id, updated_at) VALUES ($1, $2)`, id, now)
		exec(`INSERT INTO agent_sessions (agent_id, token_hash, expire_at, created_at, last_seen_at) VALUES ($1, $2, $3, $3, $3)`, id, fmt.Sprintf("hash-%d", id), now)
		exec(`INSERT INTO raw_items (item_id, author_agent_id, raw_content, created_at) VALUES ($1, $1, $2, $3)`, id, fmt.Sprintf("broadcast %d", id), now)
		exec(`INSERT INTO processed_items (item_id, updated_at) VALUES ($1, $2)`, id, now)
		exec(`INSERT INTO agent_context_revisions (agent_id, revision, compiled_context, generated_at) VALUES ($1, 1, '{}', $2)`, id, now)
		exec(`INSERT INTO agent_onboarding_v2 (agent_id, state, active_context_revision, completed_at, created_at, updated_at) VALUES ($1, 'completed', 1, $2, $2, $2)`, id, now)
	}
	exec(`INSERT INTO conversations (conv_id, participant_a, participant_b, initiator_id, last_sender_id, updated_at) VALUES ($1, $2, $3, $3, $3, $4)`, base, target, bystander, now)
	exec(`INSERT INTO private_messages (msg_id, conv_id, sender_id, receiver_id, content, created_at) VALUES ($1, $1, $2, $3, 'welcome', $4)`, base, bystander, target, now)
	exec(`INSERT INTO user_relations (from_uid, to_uid, rel_type, created_at) VALUES ($1, $2, 1, $3), ($2, $1, 1, $3)`, target, bystander, now)
	exec(`INSERT INTO auth_email_challenges (challenge_id, login_method, email, code_hash, expire_at, created_at) VALUES ($1, 'email', $2, 'x', $3, $3)`, fmt.Sprintf("ch-%d", base), email, now)
	// A CLI account switch references agents, principals and sessions without
	// ON DELETE CASCADE, so it blocks a bare DELETE FROM agents.
	var principal int64
	if err := db.QueryRowContext(ctx, `INSERT INTO agent_principals (agent_id, key_type, key_fingerprint, public_key, status, created_at, last_seen_at)
		VALUES ($1, 'ed25519-v1', $2, decode(repeat('11', 32), 'hex'), 'active', $3, $3) RETURNING principal_id`, target, fmt.Sprint(base), now).Scan(&principal); err != nil {
		t.Fatal(err)
	}
	session := fmt.Sprintf("efcs_%d", base)
	exec(`INSERT INTO console_v2_sessions (session_id, session_secret_hash, agent_id, principal_id, csrf_secret_hash, status, scopes, issued_at, idle_expires_at, absolute_expires_at, last_seen_at, auth_method)
		VALUES ($1, 's', $2, $3, 'c', 'active', ARRAY['console:read'], $4, $5, $5, $4, 'handoff')`, session, target, principal, now, now+3600000)
	exec(`INSERT INTO agent_cli_account_switches (switch_id_hash, source_agent_id, principal_id, source_console_session_id, status, expires_at, created_at)
		VALUES ($1, $2, $3, $4, 'pending_target', $5, $6)`, fmt.Sprintf("sw-%d", base), target, principal, session, now+3600000, now)

	if _, err := ResetPostgres(ctx, db, otherEmail, testGuard, true); !errors.Is(err, ErrNotTestAccount) {
		t.Fatalf("non-test email: got %v, want ErrNotTestAccount", err)
	}

	exec(`UPDATE agents SET is_official = TRUE WHERE agent_id = $1`, target)
	if _, err := ResetPostgres(ctx, db, email, testGuard, true); !errors.Is(err, ErrOfficial) {
		t.Fatalf("official account: got %v, want ErrOfficial", err)
	}
	exec(`UPDATE agents SET is_official = FALSE WHERE agent_id = $1`, target)

	plan, err := ResetPostgres(ctx, db, email, testGuard, false)
	if err != nil {
		t.Fatal(err)
	}
	if plan.AgentID != target || len(plan.TokenHashes) != 1 {
		t.Fatalf("plan = %+v", plan)
	}
	cascaded := map[string]int64{}
	for _, c := range plan.Cascaded {
		cascaded[c.Table] = c.Rows
	}
	if cascaded["agent_onboarding_v2"] != 1 || cascaded["console_v2_sessions"] != 1 || cascaded["private_messages"] != 1 {
		t.Fatalf("cascade preview = %+v", plan.Cascaded)
	}
	if len(plan.Peers) != 1 || plan.Peers[0] != bystander || len(plan.Convs) != 1 || plan.Convs[0].ID != base {
		t.Fatalf("peers = %v, convs = %v", plan.Peers, plan.Convs)
	}
	if count(`SELECT count(*) FROM agents WHERE agent_id = $1`, target) != 1 {
		t.Fatal("dry run deleted the agent")
	}

	// An order event makes the account untouchable until a person has dealt with it.
	exec(`INSERT INTO trade_order_events (event_id, order_id, event_type, actor_agent_id, created_at) VALUES ($1, $1, 1, $2, $3)`, base, target, now)
	if _, err := ResetPostgres(ctx, db, email, testGuard, true); !errors.Is(err, ErrHasTrades) {
		t.Fatalf("account with trades: got %v, want ErrHasTrades", err)
	}
	if count(`SELECT count(*) FROM agents WHERE agent_id = $1`, target) != 1 || count(`SELECT count(*) FROM raw_items WHERE author_agent_id = $1`, target) != 1 {
		t.Fatal("refused reset still deleted rows")
	}
	exec(`DELETE FROM trade_order_events WHERE event_id = $1`, base)

	report, err := ResetPostgres(ctx, db, email, testGuard, true)
	if err != nil {
		t.Fatal(err)
	}
	for i, c := range report.Deleted {
		if plan.Deleted[i] != c {
			t.Errorf("dry run said %+v, apply did %+v", plan.Deleted[i], c)
		}
	}

	for table, where := range map[string]string{
		"agents": "agent_id = $1", "agent_profiles": "agent_id = $1", "agent_settings": "agent_id = $1",
		"agent_sessions": "agent_id = $1", "raw_items": "author_agent_id = $1", "processed_items": "item_id = $1",
		"agent_onboarding_v2": "agent_id = $1", "agent_context_revisions": "agent_id = $1",
		"agent_network_memberships": "agent_id = $1", "agent_principals": "agent_id = $1",
		"console_v2_sessions": "agent_id = $1", "agent_cli_account_switches": "source_agent_id = $1",
		"conversations": "participant_a = $1 OR participant_b = $1", "private_messages": "sender_id = $1 OR receiver_id = $1",
		"user_relations": "from_uid = $1 OR to_uid = $1",
	} {
		if n := count("SELECT count(*) FROM "+table+" WHERE "+where, target); n != 0 {
			t.Errorf("%s: %d rows left for the reset agent", table, n)
		}
	}
	if n := count(`SELECT count(*) FROM auth_email_challenges WHERE email = $1`, email); n != 0 {
		t.Errorf("auth_email_challenges: %d rows left", n)
	}
	for table, where := range map[string]string{
		"agents": "agent_id = $1", "agent_profiles": "agent_id = $1", "agent_settings": "agent_id = $1",
		"agent_sessions": "agent_id = $1", "raw_items": "author_agent_id = $1", "processed_items": "item_id = $1",
		"agent_onboarding_v2": "agent_id = $1",
	} {
		if n := count("SELECT count(*) FROM "+table+" WHERE "+where, bystander); n != 1 {
			t.Errorf("%s: bystander has %d rows, want 1", table, n)
		}
	}

	again, err := ResetPostgres(ctx, db, email, testGuard, true)
	if err != nil || again.AgentID != 0 {
		t.Fatalf("second reset: report=%+v err=%v", again, err)
	}
}
