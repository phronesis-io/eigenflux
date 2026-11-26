// Command account_soft_delete deactivates one identity using an authorized
// operator database login. By default its entire transaction is read-only.
package main

import (
	"context"
	"crypto/sha256"
	"database/sql"
	"errors"
	"flag"
	"fmt"
	"os"
	"strings"
	"time"

	_ "github.com/lib/pq"
)

type request struct {
	agentID       int64
	email, ticket string
	apply         bool
}

type count struct {
	Table string
	Rows  int64
}

type report struct {
	changed, blocked []count
	alreadyDeleted   bool
}

const nowMS = "(extract(epoch from transaction_timestamp()) * 1000)::bigint"
const target = "agent_id = $1"
const ownPrincipals = "SELECT principal_id FROM agent_principals WHERE agent_id = $1"
const ownFingerprint = "SELECT key_fingerprint FROM agent_principals WHERE agent_id = $1"

type change struct{ table, set, where string }

// The same predicate drives the preview count and the conditional update.
// The account-level tombstone hides retained history. Pending work and
// independently usable authentication credentials additionally become terminal.
var changes = []change{
	{"agent_sessions", "status = 1", target + " AND status = 0"},
	{"agent_credential_sessions", "revoked_at = " + nowMS, "principal_id IN (" + ownPrincipals + ") AND revoked_at IS NULL"},
	{"agent_principals", "status = 'revoked', revoked_at = " + nowMS, target + " AND status <> 'revoked'"},
	{"agent_email_bindings", "status = 'revoked', verification_state = 'revoked', updated_at = " + nowMS + ", revoked_at = " + nowMS, target + " AND status = 'active'"},
	{"agent_bootstrap_grants", "status = 'revoked'", "key_fingerprint IN (" + ownFingerprint + ") AND status = 'issued'"},
	{"v2_email_challenges", "status = 'revoked'", "(subject_agent_id = $1 OR console_session_id IN (SELECT session_id FROM console_v2_sessions WHERE agent_id = $1)) AND status = 'pending'"},
	{"auth_email_challenges", "expire_at = " + nowMS, "lower(email) = (SELECT lower(email) FROM agents WHERE agent_id = $1) AND status = 0 AND expire_at > " + nowMS},
	{"console_v2_sessions", "status = 'revoked', revoked_at = " + nowMS, target + " AND status = 'active'"},
	{"console_v2_handoffs", "revoked_at = " + nowMS, target + " AND consumed_at IS NULL AND revoked_at IS NULL"},
	{"agent_account_recoveries", "status = 'revoked'", "(source_agent_id = $1 OR target_agent_id = $1) AND status = 'pending'"},
	{"agent_cli_account_switches", "status = 'revoked'", "(source_agent_id = $1 OR target_agent_id = $1) AND status IN ('pending_target','pending_onboarding')"},
	{"agent_network_goals", "status = 'deleted', version = version + 1, updated_at = " + nowMS, target + " AND status = 'active'"},
	{"agent_intent_actions", "status = 'deleted', version = version + 1, updated_at = " + nowMS, target + " AND status <> 'deleted'"},
	{"agent_context_heads", "active_revision = NULL, updated_at = " + nowMS, target + " AND active_revision IS NOT NULL"},
	{"agent_attention_items", "status = 'dismissed'", target + " AND status = 'open'"},
	{"agent_commands", "status = 'expired', completed_at = " + nowMS, target + " AND status IN ('pending','claimed','notified')"},
	{"agent_runtime_leases", "lease_until = " + nowMS + ", updated_at = " + nowMS, target + " AND lease_until > " + nowMS},
	{"control_wakeup_outbox", "status = 'dead'", target + " AND status = 'pending'"},
	{"agent_settings", "recurring_publish = false, auto_reply_pm = false, auto_comment = false, show_add_friend = false, official_pm_optout = true, updated_at = " + nowMS, target},
}

// These workflows have remote state and require independent retirement first.
var externalDependencies = []struct{ table, where string }{
	{"trading_services", "seller_agent_id = $1 AND status <> 2"},
	{"trade_orders", "buyer_agent_id = $1 OR seller_agent_id = $1"},
	{"integration_agents", target},
	{"raw_items", "author_agent_id = $1"},
}

func deactivate(ctx context.Context, db *sql.DB, req request) (report, error) {
	var result report
	req.email = strings.ToLower(strings.TrimSpace(req.email))
	req.ticket = strings.TrimSpace(req.ticket)
	if req.agentID <= 0 || req.email == "" || (req.apply && req.ticket == "") {
		return result, errors.New("require --agent-id, --email and, for --apply, --ticket")
	}
	tx, err := db.BeginTx(ctx, &sql.TxOptions{ReadOnly: !req.apply, Isolation: sql.LevelSerializable})
	if err != nil {
		return result, err
	}
	defer tx.Rollback()
	if _, err = tx.ExecContext(ctx, "SET LOCAL search_path = pg_catalog, public; SET LOCAL lock_timeout = '5s'; SET LOCAL statement_timeout = '30s'"); err != nil {
		return result, err
	}
	var allowed bool
	if err = tx.QueryRowContext(ctx, "SELECT public.eigenflux_can_soft_delete_account()").Scan(&allowed); err != nil {
		return result, err
	}
	if !allowed {
		return result, errors.New("explicit account erasure operator membership required")
	}
	query := "SELECT identity_state, lower(email) = $2, email_kind FROM agents WHERE agent_id = $1"
	if req.apply {
		query += " FOR UPDATE"
	}
	var state, kind string
	var emailMatch bool
	if err = tx.QueryRowContext(ctx, query, req.agentID, req.email).Scan(&state, &emailMatch, &kind); err != nil {
		return result, fmt.Errorf("locate account: %w", err)
	}
	digest := sha256.Sum256([]byte(req.email))
	if state == "deleted" {
		if err = tx.QueryRowContext(ctx, "SELECT EXISTS(SELECT FROM account_soft_delete_audit WHERE agent_id=$1 AND email_digest=$2)", req.agentID, digest[:]).Scan(&result.alreadyDeleted); err != nil {
			return result, err
		}
		if !result.alreadyDeleted {
			return result, errors.New("deleted account does not match the original audited email")
		}
		return result, tx.Commit()
	}
	if state != "active" || !emailMatch {
		return result, errors.New("active account ID and email do not match")
	}
	var bindings, matching int
	if err = tx.QueryRowContext(ctx, "SELECT count(*), count(*) FILTER (WHERE normalized_email=$2) FROM agent_email_bindings WHERE agent_id=$1 AND status='active'", req.agentID, req.email).Scan(&bindings, &matching); err != nil {
		return result, err
	}
	if bindings != matching || (kind == "v2_bound" && matching != 1) || kind == "internal_alias" {
		return result, errors.New("current email binding does not match")
	}
	for _, dep := range externalDependencies {
		var n int64
		if err = tx.QueryRowContext(ctx, "SELECT count(*) FROM "+dep.table+" WHERE "+dep.where, req.agentID).Scan(&n); err != nil {
			return result, fmt.Errorf("check %s: %w", dep.table, err)
		}
		if n > 0 {
			result.blocked = append(result.blocked, count{dep.table, n})
		}
	}
	if req.apply && len(result.blocked) > 0 {
		return result, errors.New("account has external dependencies; no changes applied")
	}
	for _, step := range changes {
		var n int64
		if req.apply {
			var updated sql.Result
			updated, err = tx.ExecContext(ctx, "UPDATE "+step.table+" SET "+step.set+" WHERE "+step.where, req.agentID)
			if err == nil {
				n, err = updated.RowsAffected()
			}
		} else {
			err = tx.QueryRowContext(ctx, "SELECT count(*) FROM "+step.table+" WHERE "+step.where, req.agentID).Scan(&n)
		}
		if err != nil {
			return result, fmt.Errorf("%s: %w", step.table, err)
		}
		result.changed = append(result.changed, count{step.table, n})
	}
	if req.apply {
		if _, err = tx.ExecContext(ctx, "INSERT INTO account_soft_delete_audit (agent_id,operator_name,email_digest,ticket) VALUES ($1,session_user,$2,$3)", req.agentID, digest[:], req.ticket); err != nil {
			return result, err
		}
		if _, err = tx.ExecContext(ctx, "UPDATE agents SET identity_state='deleted',email=$2,email_kind='internal_alias',email_verified_at=NULL,updated_at="+nowMS+" WHERE agent_id=$1", req.agentID, fmt.Sprintf("deleted-%d@identity.invalid", req.agentID)); err != nil {
			return result, err
		}
	}
	result.changed = append(result.changed, count{"agents", 1})
	return result, tx.Commit()
}

func main() {
	var req request
	flag.Int64Var(&req.agentID, "agent-id", 0, "target Agent ID")
	flag.StringVar(&req.email, "email", "", "current account email")
	flag.StringVar(&req.ticket, "ticket", "", "internal request reference (required for apply)")
	flag.BoolVar(&req.apply, "apply", false, "commit account deactivation (default: read-only preview)")
	flag.Parse()
	if flag.NArg() != 0 {
		fmt.Fprintln(os.Stderr, "unexpected positional arguments")
		os.Exit(2)
	}
	dsn := os.Getenv("EIGENFLUX_OPS_PG_DSN")
	if dsn == "" {
		fmt.Fprintln(os.Stderr, "EIGENFLUX_OPS_PG_DSN is required")
		os.Exit(2)
	}
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		fmt.Fprintln(os.Stderr, "invalid operator database configuration")
		os.Exit(1)
	}
	defer db.Close()
	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()
	r, err := deactivate(ctx, db, req)
	for _, c := range r.blocked {
		fmt.Printf("external dependency %s=%d\n", c.Table, c.Rows)
	}
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	if r.alreadyDeleted {
		fmt.Printf("agent_id=%d already soft-deleted\n", req.agentID)
		return
	}
	mode := "preview"
	if req.apply {
		mode = "committed"
	}
	fmt.Printf("agent_id=%d mode=%s\n", req.agentID, mode)
	for _, c := range r.changed {
		if c.Rows > 0 {
			fmt.Printf("%s=%d\n", c.Table, c.Rows)
		}
	}
}
