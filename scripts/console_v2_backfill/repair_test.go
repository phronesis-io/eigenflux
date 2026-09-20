package main

import (
	"database/sql"
	"encoding/json"
	"fmt"
	"net/url"
	"os"
	"strings"
	"testing"
	"time"
)

func TestLegacyNetworkGoal(t *testing.T) {
	cases := []struct{ name, bio, want string }{
		{"english", `Domains: security\nPurpose: research`, `Domains: security\nPurpose: research`},
		{"chinese unchanged", "  关注安全研究与技术交流  ", "延续现有 Agent 方向：关注安全研究与技术交流"},
		{"mixed language unchanged", "Security 安全研究", "延续现有 Agent 方向：Security 安全研究"},
		{"other language", "Recherche en sécurité", "Recherche en sécurité"},
		{"line endings unchanged", "first\\r\\nsecond\r\nthird", "first\\r\\nsecond\r\nthird"},
		{"windows path unchanged", `Workspace: C:\new-project`, `Workspace: C:\new-project`},
		{"not unescape other text", `code: \t and \u4e00`, `code: \t and \u4e00`},
		{"empty", "  \n ", "Continue existing EigenFlux network activities and collaboration."},
		{"preserve truncation", strings.Repeat("x", 426), strings.Repeat("x", 240)},
		{"unicode limit", strings.Repeat("é", 241), strings.Repeat("é", 240)},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := legacyNetworkGoal(legacyAgent{bio: tc.bio}); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}

func repairTestDB(t *testing.T) *sql.DB {
	t.Helper()
	dsn := os.Getenv("EIGENFLUX_GOAL_REPAIR_TEST_DSN")
	if dsn == "" {
		t.Skip("EIGENFLUX_GOAL_REPAIR_TEST_DSN is required for PostgreSQL repair tests")
	}
	u, err := url.Parse(dsn)
	if err != nil || (u.Hostname() != "127.0.0.1" && u.Hostname() != "localhost" && u.Hostname() != "::1") {
		t.Fatal("repair tests require an explicit loopback PostgreSQL URL")
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		t.Fatal(err)
	}
	db.SetMaxOpenConns(1)
	schema := fmt.Sprintf("goal_repair_%d", time.Now().UnixNano())
	execTestSQL(t, db, "CREATE SCHEMA "+schema)
	t.Cleanup(func() {
		_, _ = db.Exec("SET default_transaction_read_only = off")
		_, _ = db.Exec("DROP SCHEMA " + schema + " CASCADE")
		_ = db.Close()
	})
	execTestSQL(t, db, "SET search_path TO "+schema)
	execTestSQL(t, db, "CREATE TABLE agents (agent_id bigint PRIMARY KEY)")
	migration, err := os.ReadFile("../../migrations/000065_console_v2_foundation.sql")
	if err != nil {
		t.Fatal(err)
	}
	for _, table := range []string{"agent_network_goals", "agent_context_heads", "agent_context_revisions", "agent_onboarding_v2", "agent_onboarding_drafts"} {
		start := strings.Index(string(migration), "CREATE TABLE IF NOT EXISTS "+table+" (")
		if start < 0 {
			t.Fatalf("missing migration table %s", table)
		}
		ddl := string(migration)[start:]
		end := strings.Index(ddl, "\n);")
		if end < 0 {
			t.Fatalf("missing end for %s", table)
		}
		execTestSQL(t, db, ddl[:end+3])
	}
	execTestSQL(t, db, `CREATE UNIQUE INDEX unique_active_goal ON agent_network_goals (agent_id) WHERE status = 'active';
		ALTER TABLE agent_context_heads ADD FOREIGN KEY (agent_id, active_revision)
		REFERENCES agent_context_revisions(agent_id, revision) DEFERRABLE INITIALLY DEFERRED;
		ALTER TABLE agent_onboarding_v2 ADD FOREIGN KEY (agent_id, active_context_revision)
		REFERENCES agent_context_revisions(agent_id, revision) DEFERRABLE INITIALLY DEFERRED`)
	return db
}

func execTestSQL(t *testing.T, db *sql.DB, query string, args ...interface{}) {
	t.Helper()
	if _, err := db.Exec(query, args...); err != nil {
		t.Fatal(err)
	}
}

func seedRepair(t *testing.T, db *sql.DB, id int64, bio, source string) {
	t.Helper()
	text := originalLegacyNetworkGoal(legacyAgent{bio: bio})
	draft, _ := json.Marshal(map[string]interface{}{"identity_card": map[string]string{"bio": bio}, "network_goal": text})
	execTestSQL(t, db, `INSERT INTO agents VALUES ($1)`, id)
	execTestSQL(t, db, `INSERT INTO agent_network_goals
		(agent_id, goal_text, source, status, version, created_at, updated_at)
		VALUES ($1, $2, $3, 'active', 1, 100, 100)`, id, text, source)
	execTestSQL(t, db, `INSERT INTO agent_context_revisions
		SELECT agent_id, 1, jsonb_build_object('context_revision', 1,
		'network_goal', jsonb_build_object('goal_id', goal_id::text, 'text', goal_text, 'source', source, 'status', status),
		'intent_actions', '[{"intent_id":"7","then":"keep my intent"}]'::jsonb,
		'security_boundary', '{"auto_reply_pm":false}'::jsonb, 'future_field', '{"retain":true}'::jsonb), 3, 100
		FROM agent_network_goals WHERE agent_id=$1`, id)
	execTestSQL(t, db, `INSERT INTO agent_context_heads VALUES ($1, 1, 1, 100)`, id)
	execTestSQL(t, db, `INSERT INTO agent_onboarding_v2
		(agent_id,state,current_step,revision,active_context_revision,completed_at,created_at,updated_at)
		VALUES ($1,'completed',5,1,1,100,100,100)`, id)
	execTestSQL(t, db, `INSERT INTO agent_onboarding_drafts
		(agent_id,revision,draft_data,actor_type,request_id,created_at)
		VALUES ($1,1,$2::jsonb,'system_derived','legacy-backfill-v2',100)`, id, string(draft))
}

func TestRepairPostgresEndToEnd(t *testing.T) {
	db := repairTestDB(t)
	bio := `Domains: cybersecurity\nPurpose: research ` + strings.Repeat("x", 300)
	seedRepair(t, db, 1, bio, "system_derived")
	seedRepair(t, db, 2, "Original English biography", "human_edit")
	seedRepair(t, db, 3, "Keep this edited goal", "human_edit")
	seedRepair(t, db, 4, "Not a migration", "system_derived")
	seedRepair(t, db, 5, "Agent has replaced this", "agent_prefill")
	seedRepair(t, db, 6, "", "system_derived")
	seedRepair(t, db, 7, "原始中文简介", "system_derived")
	seedRepair(t, db, 8, "Security 安全研究", "human_edit")
	execTestSQL(t, db, `UPDATE agent_network_goals SET goal_text='My actual edited goal' WHERE agent_id=3`)
	execTestSQL(t, db, `UPDATE agent_onboarding_drafts SET request_id='unrelated' WHERE agent_id=4`)
	// A read-only session proves the default repair mode never writes.
	execTestSQL(t, db, `SET default_transaction_read_only = on`)
	if err := repairLegacyGoals(db, false, 1, 0); err != nil {
		t.Fatal(err)
	}
	execTestSQL(t, db, `SET default_transaction_read_only = off`)
	var n int
	if err := db.QueryRow(`SELECT count(*) FROM agent_context_revisions`).Scan(&n); err != nil || n != 8 {
		t.Fatalf("dry-run changed snapshots: n=%d err=%v", n, err)
	}
	if err := repairLegacyGoals(db, true, 1, 0); err != nil {
		t.Fatal(err)
	}
	for _, id := range []int64{1, 2} {
		var text, source string
		var active, onboard, schema, goalVersion int
		var equal, historical bool
		err := db.QueryRow(`SELECT g.goal_text,g.source,h.active_revision,o.active_context_revision,r.schema_version,g.version,
			(r.compiled_context - 'network_goal' - 'context_revision') = (old.compiled_context - 'network_goal' - 'context_revision'),
			old.compiled_context->'network_goal'->>'text' = d.draft_data->>'network_goal'
			FROM agent_network_goals g JOIN agent_context_heads h USING(agent_id)
			JOIN agent_onboarding_v2 o USING(agent_id)
			JOIN agent_context_revisions r ON r.agent_id=g.agent_id AND r.revision=h.active_revision
			JOIN agent_context_revisions old ON old.agent_id=g.agent_id AND old.revision=1
			JOIN agent_onboarding_drafts d ON d.agent_id=g.agent_id AND d.revision=1
			WHERE g.agent_id=$1 AND g.status='active'`, id).Scan(&text, &source, &active, &onboard, &schema, &goalVersion, &equal, &historical)
		if err != nil {
			t.Fatal(err)
		}
		if source != "system_derived" || active != 2 || onboard != 2 || schema != 3 || goalVersion != 2 || !equal || !historical {
			t.Fatalf("lost version/history/context invariants: %d %s %d %d %d %t %t", id, source, active, onboard, schema, equal, historical)
		}
		if id == 1 && text != strings.TrimPrefix(originalLegacyNetworkGoal(legacyAgent{bio: bio}), "延续现有 Agent 方向：") {
			t.Fatalf("changed biography body while removing prefix: %q", text)
		}
	}
	if err := db.QueryRow(`SELECT count(*) FROM agent_context_heads WHERE agent_id IN(3,4,5,6,7,8) AND active_revision=1`).Scan(&n); err != nil || n != 6 {
		t.Fatalf("modified excluded agents: n=%d err=%v", n, err)
	}
	if err := repairLegacyGoals(db, true, 2, 0); err != nil {
		t.Fatal(err)
	}
	if err := db.QueryRow(`SELECT count(*) FROM agent_context_revisions`).Scan(&n); err != nil || n != 10 {
		t.Fatalf("not idempotent: n=%d err=%v", n, err)
	}
}

func TestRepairPostgresStaleCandidateAndRollback(t *testing.T) {
	db := repairTestDB(t)
	seedRepair(t, db, 1, "Archived original biography", "human_edit")
	repairs, _, err := loadGoalRepairs(db, 0, 1, 10)
	if err != nil || len(repairs) != 1 {
		t.Fatalf("load: %v %#v", err, repairs)
	}
	// Simulate a user edit after the candidate scan and before the lock.
	execTestSQL(t, db, `UPDATE agent_network_goals SET goal_text='Concurrent user edit' WHERE agent_id=1`)
	if changed, err := applyGoalRepair(db, repairs[0], 200); err != nil || changed {
		t.Fatalf("overwrote concurrent user edit: changed=%t err=%v", changed, err)
	}
	execTestSQL(t, db, `UPDATE agent_network_goals SET goal_text=$1 WHERE agent_id=1`, repairs[0].oldText)
	// Force failure after goal replacement and revision insertion; all must roll back.
	execTestSQL(t, db, `ALTER TABLE agent_onboarding_v2 ADD CONSTRAINT fail_repair CHECK(updated_at=100)`)
	if changed, err := applyGoalRepair(db, repairs[0], 200); err == nil || changed {
		t.Fatalf("expected rollback: changed=%t err=%v", changed, err)
	}
	var goals, revisions int
	if err := db.QueryRow(`SELECT (SELECT count(*) FROM agent_network_goals),(SELECT count(*) FROM agent_context_revisions)`).Scan(&goals, &revisions); err != nil || goals != 1 || revisions != 1 {
		t.Fatalf("partial repair persisted: goals=%d revisions=%d err=%v", goals, revisions, err)
	}
	execTestSQL(t, db, `ALTER TABLE agent_onboarding_v2 DROP CONSTRAINT fail_repair`)
	execTestSQL(t, db, `UPDATE agent_context_revisions SET compiled_context=jsonb_set(compiled_context,'{network_goal,text}','"inconsistent"')`)
	if changed, err := applyGoalRepair(db, repairs[0], 200); err == nil || changed {
		t.Fatalf("expected inconsistent context rejection: changed=%t err=%v", changed, err)
	}
}
